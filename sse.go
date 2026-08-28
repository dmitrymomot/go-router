package router

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Event is one server-sent event.
//
// The writer always writes a data field, so an event with empty data still
// reaches its listener. A client drops a frame without one, which only a
// comment and a retry frame are.
type Event struct {
	// ID is the "id" field, which the client sends back in Last-Event-ID on
	// its next connection. An empty ID leaves the field out, so the client
	// keeps the ID before it; a stream cannot clear that ID again.
	ID string

	// Name is the "event" field, the name addEventListener takes. An empty
	// name leaves the field out, and the event reaches onmessage.
	Name string

	// Data is the "data" field. Every line break becomes a data line of its
	// own, so a multiline payload stays one event.
	Data string

	// Retry is the "retry" field, the reconnect delay. The writer rounds it
	// down to whole milliseconds and drops the field when that is zero.
	Retry time.Duration
}

// sseConfig holds the options of one stream.
type sseConfig struct {
	// closeEvent is the last event of a stream whose channel closed.
	closeEvent *Event

	heartbeat time.Duration
	retry     time.Duration
}

// SSEOption configures a stream. Pass one to [Base.SSE], to [ServeSSE] or to
// [NewSSEStream].
type SSEOption func(*sseConfig)

// SSEHeartbeat sends a comment every d, which keeps a proxy from closing an
// idle connection. Zero, the default, sends none. The beat is a plain ticker,
// and a client ignores every comment.
//
// Only a driver honours it: [ServeSSE] and [SSEStream.Serve] own the loop, a
// hand written loop over [Base.SSE] does not.
func SSEHeartbeat(d time.Duration) SSEOption {
	return func(c *sseConfig) { c.heartbeat = d }
}

// SSERetry asks the client to wait d before it reconnects. The stream carries
// the value once, ahead of the first event.
func SSERetry(d time.Duration) SSEOption {
	return func(c *sseConfig) { c.retry = d }
}

// SSEClose sends e as the last event of a stream whose channel closed, which is
// how a client learns the stream is over: a browser reconnects after any other
// end. Only a driver honours it, and the client still has to close its
// EventSource when it sees the event.
func SSEClose(e Event) SSEOption {
	return func(c *sseConfig) { c.closeEvent = &e }
}

// sseHeartbeatText is the comment that [SSEHeartbeat] sends.
const sseHeartbeatText = "ping"

// SSEWriter writes server-sent events to one client. [Base.SSE] returns one
// after it commits the response.
//
// Every send writes a whole frame and flushes it, and a send that fails leaves
// nothing half written. A failed write or flush closes the stream, and every
// later send reports that same failure, so a loop that watches its errors
// ends. An event the writer rejects before it writes leaves the stream open: a
// line break in the ID or the name, a value that fails to encode, a component
// that fails.
//
// A writer belongs to one request and is not safe for concurrent use. It
// points at the [Base] of that request, which [NewPooled] hands on as soon as
// the handler returns, so never keep a writer past the handler.
type SSEWriter struct {
	b   *Base
	rc  *http.ResponseController
	err error

	// The frame a send is building, kept between sends so a stream allocates
	// the room for its largest frame once.
	buf bytes.Buffer

	// lines writes the data lines of the frame that buf holds.
	lines sseLines

	cfg sseConfig

	// A HEAD request, which gets the headers alone.
	head bool
}

// SSE commits the response as a server-sent event stream and returns the writer
// for it.
//
// It sets the media type, turns off caching and proxy buffering, and clears
// the write deadline, because a stream outlives any [http.Server.WriteTimeout].
// Then it writes and flushes the header, so the EventSource of a browser fires
// its open event at once. The stream owns Cache-Control, X-Accel-Buffering and
// Connection and overwrites what the handler set; the media type stays the
// handler's, as in [Base.Render].
//
// The status belongs to the whole stream, and a browser accepts only 200. A
// response writer that cannot flush reports an error before anything commits,
// which the error handler answers with 500.
//
// The handler owns the loop and returns only when the stream is over;
// [ServeSSE] and [SSEStream.Serve] write that loop for you.
//
// A HEAD request gets the headers alone. Every send is then a no-op, so a hand
// written loop watches [SSEWriter.Closed] or the request context to stop. Both
// drivers stop by themselves.
func (b *Base) SSE(status int, opts ...SSEOption) (*SSEWriter, error) {
	if !canFlush(b.res.ResponseWriter) {
		return nil, ErrInternalServerError.WithError(
			errors.New("router: the response writer cannot flush, which a server-sent event stream needs"))
	}

	s := &SSEWriter{b: b, rc: http.NewResponseController(b.res.ResponseWriter)}
	for _, opt := range opts {
		opt(&s.cfg)
	}

	b.contentType(MIMETextEventStream)
	h := b.res.Header()
	h.Set(HeaderCacheControl, "no-cache")
	h.Set(HeaderXAccelBuffering, "no")
	if b.req.ProtoMajor == 1 {
		h.Set(HeaderConnection, "keep-alive")
	}

	// A stream lives for minutes or hours, so the per-request write deadline
	// has to go. A writer that keeps none reports ErrNotSupported.
	if err := s.rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return nil, ErrInternalServerError.WithError(fmt.Errorf("router: clear the write deadline: %w", err))
	}

	b.res.WriteHeader(status)
	s.head = b.req.Method == http.MethodHead
	if err := s.flush(); err != nil {
		return nil, err
	}
	if !s.head {
		s.resetBuf()
		if s.retryField(s.cfg.retry) {
			if err := s.commit(); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

// LastEventID returns the ID of the last event the client saw, from the
// Last-Event-ID header it sends on a reconnect. It is empty on a first
// connection. Read it to replay what the client missed.
func (b *Base) LastEventID() string { return b.req.Header.Get(HeaderLastEventID) }

// Request returns the request the stream answers, which is how a sender reads
// the client headers and the request context.
func (s *SSEWriter) Request() *http.Request { return s.b.req }

// LastEventID is [Base.LastEventID] for the request the stream answers.
func (s *SSEWriter) LastEventID() string { return s.b.LastEventID() }

// Closed reports whether the stream is over: a HEAD request opens it closed,
// and a write or a flush that failed closes it. Every send on a closed stream
// writes nothing.
func (s *SSEWriter) Closed() bool { return s.head || s.err != nil }

// Send writes one event.
func (s *SSEWriter) Send(e Event) error {
	ok, err := s.begin(e)
	if !ok {
		return err
	}
	s.lines.WriteString(e.Data)
	return s.end()
}

// SendData writes an event that carries data alone, which reaches an onmessage
// listener in the browser.
func (s *SSEWriter) SendData(data string) error { return s.Send(Event{Data: data}) }

// SendJSON writes v as the data of an event named name. It encodes straight
// into the frame, so a value that fails to encode leaves nothing on the wire.
// [Router.JSONOptions] applies, and opts overrides it.
func (s *SSEWriter) SendJSON(name string, v any, opts ...json.Options) error {
	ok, err := s.begin(Event{Name: name})
	if !ok {
		return err
	}
	if err := json.MarshalWrite(&s.lines, v, s.b.jsonOptions(opts)...); err != nil {
		return ErrInternalServerError.WithError(fmt.Errorf("router: encode server-sent event: %w", err))
	}
	return s.end()
}

// SendComponent renders c as the data of an event named name, which is how an
// htmx or a Datastar page takes a fragment over a stream.
//
// The component receives the [Base] as its context, as in [Base.Render], and
// its line breaks become data lines. The frame reaches the client only after
// the component returns, so one that fails halfway writes nothing.
func (s *SSEWriter) SendComponent(name string, c Component) error {
	ok, err := s.begin(Event{Name: name})
	if !ok {
		return err
	}
	if err := c.Render(s.b, &s.lines); err != nil {
		return renderError(err)
	}
	return s.end()
}

// Comment writes a comment, which every client ignores. It keeps an idle
// connection open, which is what [SSEHeartbeat] sends it for.
func (s *SSEWriter) Comment(text string) error {
	if s.Closed() {
		return s.err
	}
	s.resetBuf()
	s.lines = sseLines{buf: &s.buf, prefix: ": "}
	s.lines.WriteString(text)
	return s.end()
}

// begin starts a frame and writes the single line fields of e. It reports
// whether the frame started, which a closed stream and a rejected field both
// stop. A nil error is not enough on its own: a HEAD request closes a stream
// with no failure to report.
func (s *SSEWriter) begin(e Event) (bool, error) {
	if s.Closed() {
		return false, s.err
	}
	s.resetBuf()
	if err := s.field("id", e.ID); err != nil {
		return false, err
	}
	if err := s.field("event", e.Name); err != nil {
		return false, err
	}
	s.retryField(e.Retry)
	s.lines = sseLines{buf: &s.buf, prefix: "data: "}
	return true, nil
}

// resetBuf empties the frame buffer, and drops one that grew past the ceiling
// [Base.Render] pools at, so one huge event does not hold that room for hours.
func (s *SSEWriter) resetBuf() {
	if s.buf.Cap() > maxPooledRenderBuf {
		s.buf = bytes.Buffer{}
		return
	}
	s.buf.Reset()
}

// retryField writes the retry field, and reports whether the delay reached the
// whole millisecond the field carries.
func (s *SSEWriter) retryField(d time.Duration) bool {
	ms := d.Milliseconds()
	if ms <= 0 {
		return false
	}
	s.buf.WriteString("retry: ")
	s.buf.WriteString(strconv.FormatInt(ms, 10))
	s.buf.WriteByte('\n')
	return true
}

// field writes one single line field, and skips an empty value. It rejects a
// line break, which would end the field and let the rest forge fields, or
// whole events, of its own. The frame stays unwritten.
func (s *SSEWriter) field(name, value string) error {
	if value == "" {
		return nil
	}
	if strings.ContainsAny(value, "\r\n") {
		return ErrInternalServerError.WithError(
			fmt.Errorf("router: the %s of a server-sent event holds a line break: %q", name, value))
	}
	s.buf.WriteString(name)
	s.buf.WriteString(": ")
	s.buf.WriteString(value)
	s.buf.WriteByte('\n')
	return nil
}

// end terminates the data lines of the frame and writes it.
func (s *SSEWriter) end() error {
	s.lines.end()
	return s.commit()
}

// commit terminates the frame, writes it and flushes it.
func (s *SSEWriter) commit() error {
	s.buf.WriteByte('\n')
	if _, err := s.b.res.Write(s.buf.Bytes()); err != nil {
		s.err = err
		return err
	}
	return s.flush()
}

// flush sends what the writer holds to the client.
func (s *SSEWriter) flush() error {
	if err := s.rc.Flush(); err != nil {
		s.err = err
		return err
	}
	return nil
}

// finish sends the close event of the configuration, if it holds one.
func (s *SSEWriter) finish() error {
	if s.cfg.closeEvent == nil {
		return nil
	}
	return s.Send(*s.cfg.closeEvent)
}

// canFlush reports whether w, or a writer it wraps, can flush. It walks the
// same chain as [http.ResponseController], which is what the stream uses.
func canFlush(w http.ResponseWriter) bool {
	for {
		switch w.(type) {
		case interface{ FlushError() error }, http.Flusher:
			return true
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return false
		}
		w = u.Unwrap()
	}
}

// sseLines writes text as one prefixed line per line. It keeps its state
// between calls, so a component that splits a line break over two writes still
// produces two lines.
type sseLines struct {
	buf    *bytes.Buffer
	prefix string

	// A line is started and still needs its terminator.
	open bool

	// The last byte was a carriage return, so a line feed now belongs to the
	// same break.
	cr bool

	// At least one line started.
	wrote bool
}

// Write implements [io.Writer], which is what a [Component] renders into.
func (w *sseLines) Write(p []byte) (int, error) {
	for i := 0; i < len(p); {
		j := nextBreak(p, i)
		if j > i {
			w.start()
			w.buf.Write(p[i:j])
			w.cr = false
		}
		if j < len(p) {
			w.br(p[j])
			j++
		}
		i = j
	}
	return len(p), nil
}

// WriteString is [sseLines.Write] for a string, which spares a copy. It reports
// nothing: a write into a buffer cannot fail, and only [io.Writer] forces the
// pair on Write.
func (w *sseLines) WriteString(p string) {
	for i := 0; i < len(p); {
		j := nextBreak(p, i)
		if j > i {
			w.start()
			w.buf.WriteString(p[i:j])
			w.cr = false
		}
		if j < len(p) {
			w.br(p[j])
			j++
		}
		i = j
	}
}

// nextBreak returns the index of the first carriage return or line feed at or
// after i, or the length of v.
func nextBreak[T string | []byte](v T, i int) int {
	for ; i < len(v); i++ {
		if v[i] == '\n' || v[i] == '\r' {
			return i
		}
	}
	return i
}

// br handles one line break byte.
func (w *sseLines) br(c byte) {
	if c == '\n' && w.cr {
		w.cr = false
		return
	}
	w.cr = c == '\r'
	w.start()
	w.buf.WriteByte('\n')
	w.open = false
}

// start opens a line, unless one is already open.
func (w *sseLines) start() {
	if !w.open {
		w.buf.WriteString(w.prefix)
		w.open, w.wrote = true, true
	}
}

// end terminates the last line. Text that ends with a line break needs nothing;
// text that wrote no line needs one empty line, so the frame carries the field.
func (w *sseLines) end() {
	if w.open || !w.wrote {
		w.start()
		w.buf.WriteByte('\n')
		w.open = false
	}
}

// SSESender turns one value of an application channel into events on the
// stream. Write one for a payload the senders of this package do not cover, or
// to send several events for one value. A sender that reports an error ends
// the stream.
type SSESender[T any] func(s *SSEWriter, v T) error

// SSEJSON returns a sender that writes each value as JSON, under the given
// event name. An empty name reaches an onmessage listener.
func SSEJSON[T any](name string) SSESender[T] {
	return func(s *SSEWriter, v T) error { return s.SendJSON(name, v) }
}

// SSEText returns a sender that writes the text of each value, under the given
// event name. It formats with [fmt.Sprint], so a string passes through and a
// [fmt.Stringer] renders itself.
func SSEText[T any](name string) SSESender[T] {
	return func(s *SSEWriter, v T) error {
		return s.Send(Event{Name: name, Data: fmt.Sprint(v)})
	}
}

// SSEComponent returns a sender that renders each value as HTML, under the
// given event name.
//
// C is the component type the view returns, which the compiler infers. The
// parameter is there because a function type is invariant: the
// func(Post) templ.Component that the templ generator writes is not a
// func(Post) [Component].
func SSEComponent[T any, C Component](name string, view func(T) C) SSESender[T] {
	return func(s *SSEWriter, v T) error { return s.SendComponent(name, view(v)) }
}

// SSEEvents returns a sender for a channel that already carries events, which
// leaves the ID and the name of each event to the producer.
func SSEEvents() SSESender[Event] {
	return func(s *SSEWriter, e Event) error { return s.Send(e) }
}

// ServeSSE opens a server-sent event stream and feeds it from ch until the
// channel closes or the client goes away. It writes the loop that [Base.SSE]
// leaves to the handler.
//
// The sender turns each value into events: [SSEJSON], [SSEText],
// [SSEComponent] and [SSEEvents] cover the common shapes. The stream answers
// 200, the only status an EventSource takes.
//
// It returns nil when the channel closes and when the client goes away, which
// are both not server failures. A busy stream usually learns of a disconnect
// through the send that fails first, and returns that write error, which the
// error handler logs at debug level. It returns the error of a send or of the
// sender too; the response is committed by then, so nothing more reaches the
// client.
//
// It starts no goroutine, so nothing touches the context after the handler
// returns and a pooled context stays safe. The producer that fills ch has to
// watch the request context itself, or a send after a disconnect blocks it.
func ServeSSE[T any](c Context, ch <-chan T, send SSESender[T], opts ...SSEOption) error {
	if send == nil {
		return ErrInternalServerError.WithError(errors.New("router: ServeSSE needs a sender"))
	}

	s, err := c.base().SSE(http.StatusOK, opts...)
	if err != nil {
		return err
	}
	if s.Closed() {
		return nil
	}

	var beat <-chan time.Time
	if s.cfg.heartbeat > 0 {
		t := time.NewTicker(s.cfg.heartbeat)
		defer t.Stop()
		beat = t.C
	}

	done := c.Done()
	for {
		select {
		case <-done:
			return nil
		case v, ok := <-ch:
			if !ok {
				return s.finish()
			}
			if err := send(s, v); err != nil {
				return err
			}
		case <-beat:
			if err := s.Comment(sseHeartbeatText); err != nil {
				return err
			}
		}
	}
}

// SSEStream holds the sender and the options of a stream, so a route declares
// the shape of its events once and every request reuses it. A stream holds no
// request state, so several routes share one value.
//
// It is [ServeSSE] with a configuration that outlives the request. Reach for
// ServeSSE for a stream that one route owns.
type SSEStream[T any] struct {
	send SSESender[T]
	opts []SSEOption
}

// NewSSEStream returns a stream that sends with send and applies opts. It
// panics when send is nil, because a stream without one has nothing to write.
func NewSSEStream[T any](send SSESender[T], opts ...SSEOption) *SSEStream[T] {
	if send == nil {
		panic("router: NewSSEStream needs a sender")
	}
	return &SSEStream[T]{send: send, opts: slices.Clone(opts)}
}

// Serve opens the stream for one request and feeds it from ch. It answers as
// [ServeSSE] does.
func (st *SSEStream[T]) Serve(c Context, ch <-chan T) error {
	return ServeSSE(c, ch, st.send, st.opts...)
}
