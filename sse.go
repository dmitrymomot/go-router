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

// Event is one server-sent event. Data may hold line breaks, which the writer
// splits into the several data fields the format needs. Retry tells the client
// how long to wait before it reconnects.
type Event struct {
	ID    string
	Name  string
	Data  string
	Retry time.Duration
}

type sseConfig struct {
	closeEvent *Event
	heartbeat  time.Duration
	retry      time.Duration
}

// SSEOption configures a stream. See [SSEHeartbeat], [SSERetry] and
// [SSEClose].
type SSEOption func(*sseConfig)

const nilSSEOptionError = "router: an SSE option cannot be nil"

// SSEHeartbeat sends a comment every d, which holds a connection open through
// a proxy that drops an idle one. [ServeSSE] does the sending; a stream driven
// by hand calls [SSEWriter.Comment] itself.
func SSEHeartbeat(d time.Duration) SSEOption {
	return func(c *sseConfig) { c.heartbeat = d }
}

// SSERetry tells the client how long to wait before it reconnects. It goes out
// once, as the stream opens.
func SSERetry(d time.Duration) SSEOption {
	return func(c *sseConfig) { c.retry = d }
}

// SSEClose sends e as the last event, once the channel of [ServeSSE] closes.
// It suits a "done" the client watches for, because the browser reconnects on
// its own when a stream simply ends.
func SSEClose(e Event) SSEOption {
	return func(c *sseConfig) { c.closeEvent = &e }
}

const sseHeartbeatText = "ping"

// SSEWriter writes a server-sent event stream. [Base.SSE] opens one.
//
// The first failure closes the writer and every later call reports it, so a
// loop needs one error check per send and no more. [SSEWriter.Closed] reports
// whether the stream still takes events.
type SSEWriter struct {
	b     *Base
	rc    *http.ResponseController
	err   error
	buf   bytes.Buffer
	lines sseLines
	cfg   sseConfig
	head  bool
}

// SSE opens a server-sent event stream and writes status with the headers of
// the format. It clears the write deadline, so the stream outlives the
// ordinary timeout of the server.
//
// It reports an [ErrInternalServerError] when an option is nil or the response
// writer cannot flush. A HEAD request gets the headers and a writer that is
// already closed.
//
// See [ServeSSE] to drive a stream from a channel.
func (b *Base) SSE(status int, opts ...SSEOption) (*SSEWriter, error) {
	if err := validateSSEOptions(opts); err != nil {
		return nil, ErrInternalServerError.WithError(err)
	}
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

func validateSSEOptions(opts []SSEOption) error {
	for _, opt := range opts {
		if opt == nil {
			return errors.New(nilSSEOptionError)
		}
	}
	return nil
}

// LastEventID reports the Last-Event-ID header, which a client sends when it
// reconnects, so a handler can resume where the stream stopped.
func (b *Base) LastEventID() string { return b.req.Header.Get(HeaderLastEventID) }

// Request reports the request that opened the stream.
func (s *SSEWriter) Request() *http.Request { return s.b.req }

// LastEventID reports the Last-Event-ID header of the request. See
// [Base.LastEventID].
func (s *SSEWriter) LastEventID() string { return s.b.LastEventID() }

// Closed reports whether the stream stopped taking events, because a send
// failed or because the request is a HEAD.
func (s *SSEWriter) Closed() bool { return s.head || s.err != nil }

// Send writes e and flushes it to the client.
func (s *SSEWriter) Send(e Event) error {
	ok, err := s.begin(e)
	if !ok {
		return err
	}
	s.lines.WriteString(e.Data)
	return s.end()
}

// SendData writes an unnamed event carrying data.
func (s *SSEWriter) SendData(data string) error { return s.Send(Event{Data: data}) }

// SendJSON writes an event called name whose data is v as JSON. opts win over
// the options of [Router.JSONOptions].
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

// SendComponent writes an event called name whose data is c rendered as HTML,
// which is what htmx reads from a stream.
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

// Comment writes a comment, which the client ignores. It keeps a connection
// alive through a proxy that drops an idle one.
func (s *SSEWriter) Comment(text string) error {
	if s.Closed() {
		return s.err
	}
	s.resetBuf()
	s.lines = sseLines{buf: &s.buf, prefix: ": "}
	s.lines.WriteString(text)
	return s.end()
}

// The bool is not the error: a HEAD request closes the stream with no failure
// to report, and a caller must not then write into a frame that never started.
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

func (s *SSEWriter) resetBuf() {
	if s.buf.Cap() > maxPooledRenderBuf {
		s.buf = bytes.Buffer{}
		return
	}
	s.buf.Reset()
}

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

func (s *SSEWriter) field(name, value string) error {
	if value == "" {
		return nil
	}
	if name == "id" && strings.ContainsRune(value, '\x00') {
		return ErrInternalServerError.WithError(
			fmt.Errorf("router: the %s of a server-sent event holds a null character: %q", name, value))
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

func (s *SSEWriter) end() error {
	s.lines.end()
	return s.commit()
}

func (s *SSEWriter) commit() error {
	s.buf.WriteByte('\n')
	if _, err := s.b.res.Write(s.buf.Bytes()); err != nil {
		s.err = err
		return err
	}
	return s.flush()
}

func (s *SSEWriter) flush() error {
	if err := s.rc.Flush(); err != nil {
		s.err = err
		return err
	}
	return nil
}

func (s *SSEWriter) finish() error {
	if s.cfg.closeEvent == nil {
		return nil
	}
	return s.Send(*s.cfg.closeEvent)
}

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

type sseLines struct {
	buf    *bytes.Buffer
	prefix string
	open   bool
	cr     bool
	wrote  bool
}

func (w *sseLines) Write(p []byte) (int, error) {
	writeLines(w, p, (*bytes.Buffer).Write)
	return len(p), nil
}

func (w *sseLines) WriteString(p string) {
	writeLines(w, p, (*bytes.Buffer).WriteString)
}

// writeLines splits v on CR and LF and hands each run to the buffer, opening a
// field for the first and a fresh one after every break.
func writeLines[T string | []byte](w *sseLines, v T, write func(*bytes.Buffer, T) (int, error)) {
	for i := 0; i < len(v); {
		j := nextBreak(v, i)
		if j > i {
			w.start()
			//nolint:errcheck // bytes.Buffer never fails.
			write(w.buf, v[i:j])
			w.cr = false
		}
		if j < len(v) {
			w.br(v[j])
			j++
		}
		i = j
	}
}

func nextBreak[T string | []byte](v T, i int) int {
	for ; i < len(v); i++ {
		if v[i] == '\n' || v[i] == '\r' {
			return i
		}
	}
	return i
}

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

func (w *sseLines) start() {
	if !w.open {
		w.buf.WriteString(w.prefix)
		w.open, w.wrote = true, true
	}
}

func (w *sseLines) end() {
	if w.open || !w.wrote {
		w.start()
		w.buf.WriteByte('\n')
		w.open = false
	}
}

// SSESender turns one value of the channel into one event. See [SSEJSON],
// [SSEText], [SSEComponent] and [SSEEvents].
type SSESender[T any] func(s *SSEWriter, v T) error

// SSEJSON sends each value as JSON, in an event called name.
func SSEJSON[T any](name string) SSESender[T] {
	return func(s *SSEWriter, v T) error { return s.SendJSON(name, v) }
}

// SSEText sends each value in its printed form, in an event called name.
func SSEText[T any](name string) SSESender[T] {
	return func(s *SSEWriter, v T) error {
		return s.Send(Event{Name: name, Data: fmt.Sprint(v)})
	}
}

// SSEComponent renders each value through view and sends the HTML, in an event
// called name.
//
// SSEComponent panics if view is nil.
func SSEComponent[T any, C Component](name string, view func(T) C) SSESender[T] {
	if view == nil {
		panic("router: SSEComponent needs a view")
	}
	return func(s *SSEWriter, v T) error { return s.SendComponent(name, view(v)) }
}

// SSEEvents sends each [Event] of the channel as it stands, for a handler that
// sets the name, the id or the retry per event.
func SSEEvents() SSESender[Event] {
	return func(s *SSEWriter, e Event) error { return s.Send(e) }
}

// ServeSSE opens a stream and sends every value of ch through send, until ch
// closes or the client goes away. A closed ch sends the [SSEClose] event, when
// one is configured, and ends the handler.
//
// The heartbeat of [SSEHeartbeat] runs here, so a stream driven this way keeps
// itself alive.
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

// SSEStream holds a sender and its options, so several handlers can share one
// configuration. It is safe for concurrent use.
type SSEStream[T any] struct {
	send SSESender[T]
	opts []SSEOption
}

// NewSSEStream builds a stream from send and opts, which it copies.
//
// NewSSEStream panics if send is nil or an option is nil.
func NewSSEStream[T any](send SSESender[T], opts ...SSEOption) *SSEStream[T] {
	if send == nil {
		panic("router: NewSSEStream needs a sender")
	}
	if validateSSEOptions(opts) != nil {
		panic("router: NewSSEStream needs non-nil options")
	}
	return &SSEStream[T]{send: send, opts: slices.Clone(opts)}
}

// Serve is [ServeSSE] with the sender and the options of st.
func (st *SSEStream[T]) Serve(c Context, ch <-chan T) error {
	return ServeSSE(c, ch, st.send, st.opts...)
}
