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

type SSEOption func(*sseConfig)

const nilSSEOptionError = "router: an SSE option cannot be nil"

func SSEHeartbeat(d time.Duration) SSEOption {
	return func(c *sseConfig) { c.heartbeat = d }
}

func SSERetry(d time.Duration) SSEOption {
	return func(c *sseConfig) { c.retry = d }
}

func SSEClose(e Event) SSEOption {
	return func(c *sseConfig) { c.closeEvent = &e }
}

const sseHeartbeatText = "ping"

type SSEWriter struct {
	b     *Base
	rc    *http.ResponseController
	err   error
	buf   bytes.Buffer
	lines sseLines
	cfg   sseConfig
	head  bool
}

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

func (b *Base) LastEventID() string { return b.req.Header.Get(HeaderLastEventID) }

func (s *SSEWriter) Request() *http.Request { return s.b.req }

func (s *SSEWriter) LastEventID() string { return s.b.LastEventID() }

func (s *SSEWriter) Closed() bool { return s.head || s.err != nil }

func (s *SSEWriter) Send(e Event) error {
	ok, err := s.begin(e)
	if !ok {
		return err
	}
	s.lines.WriteString(e.Data)
	return s.end()
}

func (s *SSEWriter) SendData(data string) error { return s.Send(Event{Data: data}) }

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

type SSESender[T any] func(s *SSEWriter, v T) error

func SSEJSON[T any](name string) SSESender[T] {
	return func(s *SSEWriter, v T) error { return s.SendJSON(name, v) }
}

func SSEText[T any](name string) SSESender[T] {
	return func(s *SSEWriter, v T) error {
		return s.Send(Event{Name: name, Data: fmt.Sprint(v)})
	}
}

func SSEComponent[T any, C Component](name string, view func(T) C) SSESender[T] {
	if view == nil {
		panic("router: SSEComponent needs a view")
	}
	return func(s *SSEWriter, v T) error { return s.SendComponent(name, view(v)) }
}

func SSEEvents() SSESender[Event] {
	return func(s *SSEWriter, e Event) error { return s.Send(e) }
}

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

type SSEStream[T any] struct {
	send SSESender[T]
	opts []SSEOption
}

func NewSSEStream[T any](send SSESender[T], opts ...SSEOption) *SSEStream[T] {
	if send == nil {
		panic("router: NewSSEStream needs a sender")
	}
	if validateSSEOptions(opts) != nil {
		panic("router: NewSSEStream needs non-nil options")
	}
	return &SSEStream[T]{send: send, opts: slices.Clone(opts)}
}

func (st *SSEStream[T]) Serve(c Context, ch <-chan T) error {
	return ServeSSE(c, ch, st.send, st.opts...)
}
