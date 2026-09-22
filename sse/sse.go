// Package sse serves server-sent events from go-router.
//
// [Open] starts a stream and returns a [Writer] that sends events by hand.
// [Serve] drives a stream from a channel, with a [Sender] that turns each value
// into an event, as in
//
//	return sse.Serve(c, users, sse.JSON[*User]("user"), sse.Heartbeat(15*time.Second))
//
// [NewStream] holds a sender and its options for several handlers to share.
package sse

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

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/internal/routerhook"
)

// The headers that a stream reads or writes, besides those of package router.
const (
	// HeaderLastEventID is the header a client sends when it reconnects. See
	// [Writer.LastEventID].
	HeaderLastEventID = "Last-Event-Id"

	// HeaderXAccelBuffering is the header that turns off the buffering of
	// nginx. [Open] sets it to "no".
	HeaderXAccelBuffering = "X-Accel-Buffering"
)

// Event is one server-sent event. Data may hold line breaks, which the writer
// splits into the several data fields the format needs; a trailing line break
// of Data is dropped. Retry tells the client how long to wait before it
// reconnects.
type Event struct {
	ID    string
	Name  string
	Data  string
	Retry time.Duration
}

type config struct {
	closeEvent *Event
	heartbeat  time.Duration
	retry      time.Duration
}

// Option configures a stream. See [Heartbeat], [Retry] and [Close].
type Option func(*config)

const nilOptionError = "sse: an option cannot be nil"

// Heartbeat sends a comment every d, which holds a connection open through a
// proxy that drops an idle one. [Serve] does the sending; a stream driven by
// hand calls [Writer.Comment] itself.
func Heartbeat(d time.Duration) Option {
	return func(c *config) { c.heartbeat = d }
}

// Retry tells the client how long to wait before it reconnects. It goes out
// once, as the stream opens.
func Retry(d time.Duration) Option {
	return func(c *config) { c.retry = d }
}

// Close sends e as the last event, once the channel of [Serve] closes. It
// suits a "done" the client watches for, because the browser reconnects on
// its own when a stream simply ends.
func Close(e Event) Option {
	return func(c *config) { c.closeEvent = &e }
}

const heartbeatText = "ping"

// A frame buffer that grew past this is dropped rather than reused, so one
// large event does not pin its memory for the life of the stream.
const maxFrameBuf = 512 << 10

// Writer writes a server-sent event stream. [Open] opens one.
//
// A send that fails on its value returns the error, writes nothing and leaves
// the stream open: an ID or a name with a line break, an ID with a null, a
// value SendJSON cannot encode, a component that fails to render. A failed
// write or flush closes the writer, and every later call reports that failure,
// so a loop needs one error check per send and no more. [Writer.Closed]
// reports whether the stream still takes events.
type Writer struct {
	b     *router.Base
	res   *router.Response
	rc    *http.ResponseController
	err   error
	buf   bytes.Buffer
	lines lines
	cfg   config
	head  bool
}

// Open opens a server-sent event stream on c and writes status with the
// headers of the format. It clears the write deadline, so the stream outlives
// the ordinary timeout of the server.
//
// It reports a [router.ErrInternalServerError] when an option is nil or the
// response writer cannot flush. A HEAD request gets the headers and a writer
// that is already closed.
//
// See [Serve] to drive a stream from a channel.
func Open(c router.Context, status int, opts ...Option) (*Writer, error) {
	if err := validateOptions(opts); err != nil {
		return nil, router.ErrInternalServerError.WithError(err)
	}
	b := baseOf(c)
	res := b.Response()
	if !canFlush(res.ResponseWriter) {
		return nil, router.ErrInternalServerError.WithError(
			errors.New("sse: the response writer cannot flush, which a server-sent event stream needs"))
	}

	s := &Writer{b: b, res: res, rc: http.NewResponseController(res.ResponseWriter)}
	for _, opt := range opts {
		opt(&s.cfg)
	}

	h := res.Header()
	if h.Get(router.HeaderContentType) == "" {
		h.Set(router.HeaderContentType, router.MIMETextEventStream)
	}
	h.Set(router.HeaderCacheControl, "no-cache")
	h.Set(HeaderXAccelBuffering, "no")
	req := b.Request()
	if req.ProtoMajor == 1 {
		h.Set(router.HeaderConnection, "keep-alive")
	}

	if err := s.rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return nil, router.ErrInternalServerError.WithError(fmt.Errorf("sse: clear the write deadline: %w", err))
	}

	res.WriteHeader(status)
	s.head = req.Method == http.MethodHead
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

// baseOf reports the Base of c, or a Base over its request and response when
// c hides it.
func baseOf(c router.Context) *router.Base {
	if b, ok := router.FromContext(c); ok {
		return b
	}
	return router.NewBase(c.Response(), c.Request())
}

func validateOptions(opts []Option) error {
	for _, opt := range opts {
		if opt == nil {
			return errors.New(nilOptionError)
		}
	}
	return nil
}

// Request reports the request that opened the stream.
func (s *Writer) Request() *http.Request { return s.b.Request() }

// LastEventID reports the Last-Event-ID header of the request, which a client
// sends when it reconnects, so a handler can resume where the stream stopped.
func (s *Writer) LastEventID() string { return s.b.Request().Header.Get(HeaderLastEventID) }

// Closed reports whether the stream stopped taking events, because a write
// failed or because the request is a HEAD.
func (s *Writer) Closed() bool { return s.head || s.err != nil }

// Send writes e and flushes it to the client.
func (s *Writer) Send(e Event) error {
	ok, err := s.begin(e)
	if !ok {
		return err
	}
	s.lines.WriteString(e.Data)
	return s.end()
}

// SendData writes an unnamed event carrying data.
func (s *Writer) SendData(data string) error { return s.Send(Event{Data: data}) }

// SendJSON writes an event called name whose data is v as JSON. opts win over
// the options of [router.Router.JSONOptions].
func (s *Writer) SendJSON(name string, v any, opts ...json.Options) error {
	ok, err := s.begin(Event{Name: name})
	if !ok {
		return err
	}
	if err := json.MarshalWrite(&s.lines, v, routerhook.JSONOptions(s.b, opts)...); err != nil {
		return router.ErrInternalServerError.WithError(fmt.Errorf("sse: encode server-sent event: %w", err))
	}
	return s.end()
}

// SendComponent writes an event called name whose data is c rendered as HTML,
// which is what htmx reads from a stream. The hx-sse extension of htmx 4 swaps
// an unnamed event into the element that holds hx-sse:connect and dispatches a
// named one as a DOM event, so pass "" as name for a swap.
//
// A render failure that is already a [router.HTTPError] passes through; any
// other becomes a [router.ErrInternalServerError].
func (s *Writer) SendComponent(name string, c router.Component) error {
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
func (s *Writer) Comment(text string) error {
	if s.Closed() {
		return s.err
	}
	s.resetBuf()
	s.lines = lines{buf: &s.buf, prefix: ": "}
	s.lines.WriteString(text)
	return s.end()
}

func renderError(err error) error {
	if _, ok := errors.AsType[*router.HTTPError](err); ok {
		return err
	}
	return router.ErrInternalServerError.WithError(fmt.Errorf("sse: render component: %w", err))
}

// The bool is not the error: a HEAD request closes the stream with no failure
// to report, and a caller must not then write into a frame that never started.
func (s *Writer) begin(e Event) (bool, error) {
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
	s.lines = lines{buf: &s.buf, prefix: "data: "}
	return true, nil
}

func (s *Writer) resetBuf() {
	if s.buf.Cap() > maxFrameBuf {
		s.buf = bytes.Buffer{}
		return
	}
	s.buf.Reset()
}

func (s *Writer) retryField(d time.Duration) bool {
	ms := d.Milliseconds()
	if ms <= 0 {
		return false
	}
	s.buf.WriteString("retry: ")
	s.buf.WriteString(strconv.FormatInt(ms, 10))
	s.buf.WriteByte('\n')
	return true
}

func (s *Writer) field(name, value string) error {
	if value == "" {
		return nil
	}
	if name == "id" && strings.ContainsRune(value, '\x00') {
		return router.ErrInternalServerError.WithError(
			fmt.Errorf("sse: the %s of a server-sent event holds a null character: %q", name, value))
	}
	if strings.ContainsAny(value, "\r\n") {
		return router.ErrInternalServerError.WithError(
			fmt.Errorf("sse: the %s of a server-sent event holds a line break: %q", name, value))
	}
	s.buf.WriteString(name)
	s.buf.WriteString(": ")
	s.buf.WriteString(value)
	s.buf.WriteByte('\n')
	return nil
}

func (s *Writer) end() error {
	s.lines.end()
	return s.commit()
}

func (s *Writer) commit() error {
	s.buf.WriteByte('\n')
	if _, err := s.res.Write(s.buf.Bytes()); err != nil {
		s.err = err
		return err
	}
	return s.flush()
}

func (s *Writer) flush() error {
	if err := s.rc.Flush(); err != nil {
		s.err = err
		return err
	}
	return nil
}

func (s *Writer) finish() error {
	if s.cfg.closeEvent == nil {
		return nil
	}
	return s.Send(*s.cfg.closeEvent)
}

// canFlush follows Unwrap as far as router.UnwrapResponse does, so a chain of
// wrappers that loops back on itself ends in false rather than a hang.
const unwrapLimit = 16

func canFlush(w http.ResponseWriter) bool {
	for range unwrapLimit {
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
	return false
}

type lines struct {
	buf    *bytes.Buffer
	prefix string
	open   bool
	cr     bool
	wrote  bool
}

func (w *lines) Write(p []byte) (int, error) {
	writeLines(w, p, (*bytes.Buffer).Write)
	return len(p), nil
}

func (w *lines) WriteString(p string) {
	writeLines(w, p, (*bytes.Buffer).WriteString)
}

// writeLines splits v on CR and LF and hands each run to the buffer, opening a
// field for the first and a fresh one after every break.
func writeLines[T string | []byte](w *lines, v T, write func(*bytes.Buffer, T) (int, error)) {
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

func (w *lines) br(c byte) {
	if c == '\n' && w.cr {
		w.cr = false
		return
	}
	w.cr = c == '\r'
	w.start()
	w.buf.WriteByte('\n')
	w.open = false
}

func (w *lines) start() {
	if !w.open {
		w.buf.WriteString(w.prefix)
		w.open, w.wrote = true, true
	}
}

func (w *lines) end() {
	if w.open || !w.wrote {
		w.start()
		w.buf.WriteByte('\n')
		w.open = false
	}
}

// Sender turns one value of the channel into one event. See [JSON], [Text],
// [Component] and [Events].
type Sender[T any] func(s *Writer, v T) error

// JSON sends each value as JSON, in an event called name.
func JSON[T any](name string) Sender[T] {
	return func(s *Writer, v T) error { return s.SendJSON(name, v) }
}

// Text sends each value in its printed form, in an event called name.
func Text[T any](name string) Sender[T] {
	return func(s *Writer, v T) error {
		return s.Send(Event{Name: name, Data: fmt.Sprint(v)})
	}
}

// Component renders each value through view and sends the HTML, in an event
// called name. Pass "" as name for the hx-sse extension of htmx 4 to swap the
// HTML in; see [Writer.SendComponent].
//
// Component panics if view is nil.
func Component[T any, C router.Component](name string, view func(T) C) Sender[T] {
	if view == nil {
		panic("sse: Component needs a view")
	}
	return func(s *Writer, v T) error { return s.SendComponent(name, view(v)) }
}

// Events sends each [Event] of the channel as it stands, for a handler that
// sets the name, the id or the retry per event.
func Events() Sender[Event] {
	return func(s *Writer, e Event) error { return s.Send(e) }
}

// Serve opens a stream on c and sends every value of ch through send, until ch
// closes or the client goes away. A closed ch sends the [Close] event, when
// one is configured, and ends the handler.
//
// The heartbeat of [Heartbeat] runs here, so a stream driven this way keeps
// itself alive.
func Serve[T any](c router.Context, ch <-chan T, send Sender[T], opts ...Option) error {
	if send == nil {
		return router.ErrInternalServerError.WithError(errors.New("sse: Serve needs a sender"))
	}

	s, err := Open(c, http.StatusOK, opts...)
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
			if err := s.Comment(heartbeatText); err != nil {
				return err
			}
		}
	}
}

// Stream holds a sender and its options, so several handlers can share one
// configuration. It is safe for concurrent use.
type Stream[T any] struct {
	send Sender[T]
	opts []Option
}

// NewStream builds a stream from send and opts, which it copies.
//
// NewStream panics if send is nil or an option is nil.
func NewStream[T any](send Sender[T], opts ...Option) *Stream[T] {
	if send == nil {
		panic("sse: NewStream needs a sender")
	}
	if validateOptions(opts) != nil {
		panic("sse: NewStream needs non-nil options")
	}
	return &Stream[T]{send: send, opts: slices.Clone(opts)}
}

// Serve is [Serve] with the sender and the options of st.
func (st *Stream[T]) Serve(c router.Context, ch <-chan T) error {
	return Serve(c, ch, st.send, st.opts...)
}
