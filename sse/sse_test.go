package sse

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router"
)

type tctx struct{ router.Base }

func newRouter() *router.Router[*tctx] {
	return router.New(func(http.ResponseWriter, *http.Request) *tctx { return new(tctx) })
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func comp(s string) router.ComponentFunc {
	return func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, s)
		return err
	}
}

func newReq() *http.Request { return httptest.NewRequest(http.MethodGet, "/events", nil) }

func serve(h router.HandlerFunc[*tctx], req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	serveTo(h, req, rec)
	return rec
}

func serveTo(h router.HandlerFunc[*tctx], req *http.Request, w http.ResponseWriter) {
	r := newRouter()
	r.GET("/events", h)
	r.ServeHTTP(w, req)
}

func open(fn func(s *Writer) error, opts ...Option) router.HandlerFunc[*tctx] {
	return func(c *tctx) error {
		s, err := Open(c, http.StatusOK, opts...)
		if err != nil {
			return err
		}
		return fn(s)
	}
}

func TestHeaders(t *testing.T) {
	rec := serve(open(func(*Writer) error { return nil }), newReq())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	for name, want := range map[string]string{
		router.HeaderContentType:  router.MIMETextEventStream,
		router.HeaderCacheControl: "no-cache",
		HeaderXAccelBuffering:     "no",
		router.HeaderConnection:   "keep-alive",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if got := rec.Header().Get(router.HeaderContentLength); got != "" {
		t.Errorf("Content-Length = %q, want none", got)
	}
	if !rec.Flushed {
		t.Error("the header did not reach the client")
	}
}

func TestKeepsTheHandlerContentType(t *testing.T) {
	rec := serve(func(c *tctx) error {
		c.SetHeader(router.HeaderContentType, "text/event-stream; charset=utf-8")
		_, err := Open(c, http.StatusOK)
		return err
	}, newReq())

	if got := rec.Header().Get(router.HeaderContentType); got != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestNoConnectionHeaderOnHTTP2(t *testing.T) {
	req := newReq()
	req.Proto, req.ProtoMajor, req.ProtoMinor = "HTTP/2.0", 2, 0

	rec := serve(open(func(*Writer) error { return nil }), req)
	if got := rec.Header().Get(router.HeaderConnection); got != "" {
		t.Errorf("Connection = %q, want none", got)
	}
}

func TestSendWritesEveryField(t *testing.T) {
	rec := serve(open(func(s *Writer) error {
		return s.Send(Event{ID: "7", Name: "tick", Data: "hello", Retry: 2 * time.Second})
	}), newReq())

	want := "id: 7\nevent: tick\nretry: 2000\ndata: hello\n\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestSendData(t *testing.T) {
	rec := serve(open(func(s *Writer) error { return s.SendData("hello") }), newReq())

	if got, want := rec.Body.String(), "data: hello\n\n"; got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestDataLines(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{"one line", "a", "data: a\n\n"},
		{"empty", "", "data: \n\n"},
		{"line feed", "a\nb", "data: a\ndata: b\n\n"},
		{"carriage return and line feed", "a\r\nb", "data: a\ndata: b\n\n"},
		{"carriage return", "a\rb", "data: a\ndata: b\n\n"},
		{"trailing line feed", "a\n", "data: a\n\n"},
		{"trailing carriage return and line feed", "a\r\n", "data: a\n\n"},
		{"leading line feed", "\na", "data: \ndata: a\n\n"},
		{"empty line inside", "a\n\nb", "data: a\ndata: \ndata: b\n\n"},
		{"three lines", "a\nb\nc", "data: a\ndata: b\ndata: c\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(open(func(s *Writer) error {
				return s.Send(Event{Data: tt.data})
			}), newReq())

			if got := rec.Body.String(); got != tt.want {
				t.Errorf("frame = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestComment(t *testing.T) {
	rec := serve(open(func(s *Writer) error { return s.Comment("ping") }), newReq())

	if got, want := rec.Body.String(), ": ping\n\n"; got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestCommentLines(t *testing.T) {
	rec := serve(open(func(s *Writer) error { return s.Comment("one\ntwo") }), newReq())

	if got, want := rec.Body.String(), ": one\n: two\n\n"; got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestSendJSON(t *testing.T) {
	rec := serve(open(func(s *Writer) error {
		return s.SendJSON("user", map[string]string{"id": "7"})
	}), newReq())

	if got, want := rec.Body.String(), "event: user\ndata: {\"id\":\"7\"}\n\n"; got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestSendJSONErrorWritesNothing(t *testing.T) {
	rec := serve(open(func(s *Writer) error {
		if err := s.SendData("first"); err != nil {
			return err
		}
		err := s.SendJSON("bad", make(chan int))
		if err == nil {
			t.Error("SendJSON reported no error for a channel")
		}
		if got := router.StatusOf(err); got != http.StatusInternalServerError {
			t.Errorf("status of the error = %d, want 500", got)
		}
		return nil
	}), newReq())

	if got, want := rec.Body.String(), "data: first\n\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestSendComponent(t *testing.T) {
	rec := serve(open(func(s *Writer) error {
		return s.SendComponent("row", comp("<li>one</li>"))
	}), newReq())

	if got, want := rec.Body.String(), "event: row\ndata: <li>one</li>\n\n"; got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestSendComponentSplitWrites(t *testing.T) {
	parts := []string{"<ul>\r", "\n<li>one</li>\n", "</ul>"}
	rec := serve(open(func(s *Writer) error {
		return s.SendComponent("list", router.ComponentFunc(func(_ context.Context, w io.Writer) error {
			for _, p := range parts {
				if _, err := w.Write([]byte(p)); err != nil {
					return err
				}
			}
			return nil
		}))
	}), newReq())

	want := "event: list\ndata: <ul>\ndata: <li>one</li>\ndata: </ul>\n\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestSendComponentPassesTheContext(t *testing.T) {
	r := newRouter()
	r.GET("/u/{id}/events", open(func(s *Writer) error {
		return s.SendComponent("row", router.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			b, ok := router.FromContext(ctx)
			if !ok {
				return io.ErrUnexpectedEOF
			}
			_, err := io.WriteString(w, b.Param("id"))
			return err
		}))
	}))

	if got, want := do(r, http.MethodGet, "/u/7/events").Body.String(), "event: row\ndata: 7\n\n"; got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestSendComponentErrorWritesNothing(t *testing.T) {
	rec := serve(open(func(s *Writer) error {
		if err := s.SendData("first"); err != nil {
			return err
		}
		return s.SendComponent("row", router.ComponentFunc(func(_ context.Context, w io.Writer) error {
			if _, err := io.WriteString(w, "<li>partial"); err != nil {
				return err
			}
			return io.ErrUnexpectedEOF
		}))
	}), newReq())

	if strings.Contains(rec.Body.String(), "partial") {
		t.Errorf("the body leaked the partial frame: %q", rec.Body.String())
	}
	if got, want := rec.Body.String(), "data: first\n\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestSendComponentKeepsAnHTTPError(t *testing.T) {
	var got error
	serve(open(func(s *Writer) error {
		got = s.SendComponent("row", router.ComponentFunc(func(context.Context, io.Writer) error {
			return router.ErrNotFound
		}))
		return nil
	}), newReq())

	if !errors.Is(got, router.ErrNotFound) {
		t.Errorf("error = %v, want the 404 of the component", got)
	}
}

func TestFieldWithALineBreak(t *testing.T) {
	for _, e := range []Event{
		{ID: "7\nevent: forged", Data: "x"},
		{Name: "tick\ndata: forged", Data: "x"},
		{ID: "7\rx", Data: "x"},
	} {
		rec := serve(open(func(s *Writer) error {
			err := s.Send(e)
			if err == nil {
				t.Errorf("Send(%+v) reported no error", e)
			}
			if got := router.StatusOf(err); got != http.StatusInternalServerError {
				t.Errorf("status of the error = %d, want 500", got)
			}
			return nil
		}), newReq())

		if rec.Body.Len() != 0 {
			t.Errorf("body = %q, want empty", rec.Body.String())
		}
	}
}

func TestEventIDWithNull(t *testing.T) {
	for _, id := range []string{"\x00", "7\x00x"} {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			rec := serve(open(func(s *Writer) error {
				err := s.Send(Event{ID: id, Data: "x"})
				if err == nil {
					t.Errorf("Send took ID %q", id)
				}
				if got := router.StatusOf(err); got != http.StatusInternalServerError {
					t.Errorf("status of the error = %d, want 500", got)
				}
				return s.SendData("still open")
			}), newReq())

			if got, want := rec.Body.String(), "data: still open\n\n"; got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
		})
	}
}

func TestSendAfterAFailure(t *testing.T) {
	want := errors.New("connection reset")
	var first, second error

	serveTo(open(func(s *Writer) error {
		first, second = s.Send(Event{Data: "one"}), s.Send(Event{Data: "two"})
		if !s.Closed() {
			t.Error("the writer reports the stream as open after a failed send")
		}
		return nil
	}), newReq(), failWriter{ResponseRecorder: httptest.NewRecorder(), err: want})

	if !errors.Is(first, want) {
		t.Errorf("first send = %v, want %v", first, want)
	}
	if !errors.Is(second, want) {
		t.Errorf("second send = %v, want %v", second, want)
	}
}

func TestHEAD(t *testing.T) {
	var (
		isOpen           bool
		sendErr, commErr error
		reached          bool
	)

	r := newRouter()
	r.GET("/events", open(func(s *Writer) error {
		isOpen = !s.Closed()
		sendErr = s.Send(Event{Data: "hello"})
		commErr = s.Comment("ping")
		reached = true
		return nil
	}, Retry(time.Second)))

	rec := do(r, http.MethodHead, "/events")
	if !reached {
		t.Fatal("the handler did not reach its end, so a send failed hard")
	}
	if isOpen {
		t.Error("the writer reports the stream as open for a HEAD request")
	}
	if sendErr != nil {
		t.Errorf("Send reported %v, want no error", sendErr)
	}
	if commErr != nil {
		t.Errorf("Comment reported %v, want no error", commErr)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get(router.HeaderContentType); got != router.MIMETextEventStream {
		t.Errorf("Content-Type = %q", got)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}

func TestUnflushableWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	serveTo(open(func(*Writer) error {
		t.Error("Open returned a writer for a response writer that cannot flush")
		return nil
	}), newReq(), noFlush{ResponseWriter: rec})

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestFlushThroughAWrapper(t *testing.T) {
	rec := httptest.NewRecorder()
	serveTo(open(func(s *Writer) error {
		return s.SendData("hello")
	}), newReq(), unwrapWriter{ResponseWriter: rec})

	if got, want := rec.Body.String(), "data: hello\n\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if !rec.Flushed {
		t.Error("the event did not reach the client")
	}
}

func TestRetryOption(t *testing.T) {
	rec := serve(open(func(s *Writer) error {
		return s.SendData("hello")
	}, Retry(3*time.Second)), newReq())

	if got, want := rec.Body.String(), "retry: 3000\n\ndata: hello\n\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestRetryBelowAMillisecond(t *testing.T) {
	rec := serve(open(func(s *Writer) error {
		return s.SendData("hello")
	}, Retry(time.Microsecond)), newReq())

	if got, want := rec.Body.String(), "data: hello\n\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestLastEventID(t *testing.T) {
	req := newReq()
	req.Header.Set(HeaderLastEventID, "42")

	rec := serve(func(c *tctx) error {
		s, err := Open(c, http.StatusOK)
		if err != nil {
			return err
		}
		if got := s.LastEventID(); got != "42" {
			t.Errorf("Writer.LastEventID = %q, want %q", got, "42")
		}
		if got := s.Request(); got != c.Request() {
			t.Error("Writer.Request returned another request")
		}
		return nil
	}, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestLastEventIDIsEmptyOnAFirstConnection(t *testing.T) {
	serve(open(func(s *Writer) error {
		if got := s.LastEventID(); got != "" {
			t.Errorf("LastEventID = %q, want empty", got)
		}
		return nil
	}), newReq())
}

type failWriter struct {
	*httptest.ResponseRecorder
	err error
}

func (w failWriter) Write([]byte) (int, error) { return 0, w.err }

type noFlush struct{ http.ResponseWriter }

type unwrapWriter struct{ http.ResponseWriter }

func (w unwrapWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type stamp struct{ n int }

func (s stamp) String() string { return fmt.Sprintf("n=%d", s.n) }

func closedChan[T any](vs ...T) <-chan T {
	ch := make(chan T, len(vs))
	for _, v := range vs {
		ch <- v
	}
	close(ch)
	return ch
}

func TestServeDrainsTheChannel(t *testing.T) {
	rec := serve(func(c *tctx) error {
		return Serve(c, closedChan("one", "two"), Text[string]("msg"))
	}, newReq())

	want := "event: msg\ndata: one\n\nevent: msg\ndata: two\n\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestServeReportsNoErrorAtTheEnd(t *testing.T) {
	var got error
	serve(func(c *tctx) error {
		got = Serve(c, closedChan[string](), Text[string]("msg"))
		return got
	}, newReq())

	if got != nil {
		t.Errorf("Serve = %v, want nil", got)
	}
}

func TestServeStopsWhenTheClientGoesAway(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var got error
	rec := serve(func(c *tctx) error {
		got = Serve(c, make(chan string), Text[string]("msg"))
		return got
	}, newReq().WithContext(ctx))

	if got != nil {
		t.Errorf("Serve = %v, want nil", got)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestServeHeartbeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ch := make(chan string)
		done := make(chan struct{})
		rec := httptest.NewRecorder()

		go func() {
			defer close(done)
			serveTo(func(c *tctx) error {
				return Serve(c, ch, Text[string]("msg"), Heartbeat(time.Second))
			}, newReq(), rec)
		}()

		time.Sleep(3500 * time.Millisecond)
		synctest.Wait()
		close(ch)
		<-done

		if got := strings.Count(rec.Body.String(), ": ping\n\n"); got != 3 {
			t.Errorf("%d heartbeats, want 3, in %q", got, rec.Body.String())
		}
	})
}

func TestServeNoHeartbeatByDefault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ch := make(chan string)
		done := make(chan struct{})
		rec := httptest.NewRecorder()

		go func() {
			defer close(done)
			serveTo(func(c *tctx) error {
				return Serve(c, ch, Text[string]("msg"))
			}, newReq(), rec)
		}()

		time.Sleep(time.Minute)
		synctest.Wait()
		close(ch)
		<-done

		if rec.Body.Len() != 0 {
			t.Errorf("body = %q, want empty", rec.Body.String())
		}
	})
}

func TestServeCloseOption(t *testing.T) {
	rec := serve(func(c *tctx) error {
		return Serve(c, closedChan("one"), Text[string]("msg"),
			Close(Event{Name: "close", Data: "done"}))
	}, newReq())

	want := "event: msg\ndata: one\n\nevent: close\ndata: done\n\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestServeCloseOptionSkippedOnDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	rec := serve(func(c *tctx) error {
		return Serve(c, make(chan string), Text[string]("msg"),
			Close(Event{Name: "close"}))
	}, newReq().WithContext(ctx))

	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}

func TestServeSenderError(t *testing.T) {
	want := errors.New("render failed")
	var got error

	rec := serve(func(c *tctx) error {
		got = Serve(c, closedChan("one"), func(*Writer, string) error { return want })
		return got
	}, newReq())

	if !errors.Is(got, want) {
		t.Errorf("Serve = %v, want %v", got, want)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want the 200 of the committed stream", rec.Code)
	}
}

func TestServeWithoutASender(t *testing.T) {
	rec := serve(func(c *tctx) error {
		return Serve(c, closedChan("one"), nil)
	}, newReq())

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestServeHEAD(t *testing.T) {
	ch := make(chan string, 1)
	ch <- "one"

	r := newRouter()
	r.GET("/events", func(c *tctx) error { return Serve(c, ch, Text[string]("msg")) })

	rec := do(r, http.MethodHead, "/events")
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	if len(ch) != 1 {
		t.Error("the stream read a value for a HEAD request")
	}
}

func TestSenders(t *testing.T) {
	t.Run("JSON", func(t *testing.T) {
		rec := serve(func(c *tctx) error {
			return Serve(c, closedChan(map[string]int{"n": 1}), JSON[map[string]int]("count"))
		}, newReq())

		if got, want := rec.Body.String(), "event: count\ndata: {\"n\":1}\n\n"; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})

	t.Run("text of a Stringer", func(t *testing.T) {
		rec := serve(func(c *tctx) error {
			return Serve(c, closedChan(stamp{n: 4}), Text[stamp](""))
		}, newReq())

		if got, want := rec.Body.String(), "data: n=4\n\n"; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})

	t.Run("component", func(t *testing.T) {
		rec := serve(func(c *tctx) error {
			return Serve(c, closedChan("one"), Component("row", card))
		}, newReq())

		if got, want := rec.Body.String(), "event: row\ndata: <li>one</li>\n\n"; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})

	t.Run("events", func(t *testing.T) {
		rec := serve(func(c *tctx) error {
			return Serve(c, closedChan(Event{ID: "1", Name: "tick", Data: "one"}), Events())
		}, newReq())

		if got, want := rec.Body.String(), "id: 1\nevent: tick\ndata: one\n\n"; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})
}

func TestStreamServe(t *testing.T) {
	stream := NewStream(Component("row", card), Retry(time.Second))

	for _, item := range []string{"one", "two"} {
		rec := serve(func(c *tctx) error {
			return stream.Serve(c, closedChan(item))
		}, newReq())

		want := "retry: 1000\n\nevent: row\ndata: <li>" + item + "</li>\n\n"
		if got := rec.Body.String(); got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	}
}

func TestNewStreamWithoutASender(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewStream took a nil sender")
		}
	}()
	NewStream[string](nil)
}

func TestRejectsANilOptionBeforeCommitting(t *testing.T) {
	rec := httptest.NewRecorder()
	b := router.NewBase(rec, newReq())

	_, err := Open(b, http.StatusOK, nil)
	if cause := errors.Unwrap(err); cause == nil || cause.Error() != nilOptionError {
		t.Errorf("Open cause = %v, want %q", cause, nilOptionError)
	}
	if b.Response().Committed {
		t.Error("Open committed the response before rejecting the option")
	}
}

func TestServeRejectsANilOption(t *testing.T) {
	var got error
	serve(func(c *tctx) error {
		got = Serve(c, closedChan("one"), Text[string]("msg"), nil)
		return got
	}, newReq())

	if cause := errors.Unwrap(got); cause == nil || cause.Error() != nilOptionError {
		t.Errorf("Serve cause = %v, want %q", cause, nilOptionError)
	}
}

func TestNewStreamRejectsANilOption(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewStream took a nil option")
		}
	}()
	NewStream(Text[string]("msg"), nil)
}

func TestComponentNeedsAView(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Component took a nil view")
		}
	}()
	_ = Component[string, router.Component]("row", nil)
}

func TestOverAServer(t *testing.T) {
	ch := make(chan string)
	handlerErr := make(chan error, 1)

	r := newRouter()
	r.GET("/events", func(c *tctx) error {
		err := Serve(c, ch, Text[string]("msg"), Retry(2*time.Second))
		handlerErr <- err
		return err
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	if got := res.Header.Get(router.HeaderContentType); got != router.MIMETextEventStream {
		t.Errorf("Content-Type = %q, want %q", got, router.MIMETextEventStream)
	}

	br := bufio.NewReader(res.Body)
	readLine := func() string {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read the stream: %v", err)
		}
		return line
	}

	for _, want := range []string{"retry: 2000\n", "\n"} {
		if got := readLine(); got != want {
			t.Fatalf("line = %q, want %q", got, want)
		}
	}

	select {
	case ch <- "hello":
	case <-time.After(5 * time.Second):
		t.Fatal("the handler is not reading the channel")
	}
	for _, want := range []string{"event: msg\n", "data: hello\n", "\n"} {
		if got := readLine(); got != want {
			t.Fatalf("line = %q, want %q", got, want)
		}
	}

	_ = res.Body.Close()

	select {
	case err := <-handlerErr:
		if err != nil {
			t.Errorf("the handler reported %v, want nil after the client went away", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not return after the client went away")
	}
}

type flushErrWriter struct {
	*httptest.ResponseRecorder
	err error
}

func (w flushErrWriter) FlushError() error { return w.err }

type deadlineWriter struct {
	*httptest.ResponseRecorder
	err error
}

func (w deadlineWriter) SetWriteDeadline(time.Time) error { return w.err }

func TestFlushError(t *testing.T) {
	want := errors.New("stream reset")
	var opened, sent error

	serveTo(func(c *tctx) error {
		s, err := Open(c, http.StatusOK)
		opened = err
		if err != nil {
			return nil
		}
		sent = s.Send(Event{Data: "one"})
		return nil
	}, newReq(), flushErrWriter{ResponseRecorder: httptest.NewRecorder(), err: want})

	if !errors.Is(opened, want) {
		t.Errorf("Open = %v, want %v", opened, want)
	}
	if sent != nil {
		t.Errorf("Send = %v, want no send at all", sent)
	}
}

func TestWriteDeadlineError(t *testing.T) {
	rec := httptest.NewRecorder()
	serveTo(open(func(*Writer) error {
		t.Error("Open returned a writer although the deadline stayed")
		return nil
	}), newReq(), deadlineWriter{ResponseRecorder: rec, err: errors.New("no")})

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestRetryWriteError(t *testing.T) {
	want := errors.New("connection reset")
	var opened error

	serveTo(func(c *tctx) error {
		_, opened = Open(c, http.StatusOK, Retry(time.Second))
		return nil
	}, newReq(), failWriter{ResponseRecorder: httptest.NewRecorder(), err: want})

	if !errors.Is(opened, want) {
		t.Errorf("Open = %v, want %v", opened, want)
	}
}

func TestEverySendReportsTheFailure(t *testing.T) {
	want := errors.New("connection reset")

	serveTo(open(func(s *Writer) error {
		if err := s.Send(Event{Data: "one"}); !errors.Is(err, want) {
			t.Errorf("Send = %v, want %v", err, want)
		}
		if err := s.SendJSON("x", 1); !errors.Is(err, want) {
			t.Errorf("SendJSON = %v, want %v", err, want)
		}
		if err := s.SendComponent("x", comp("<p/>")); !errors.Is(err, want) {
			t.Errorf("SendComponent = %v, want %v", err, want)
		}
		if err := s.Comment("ping"); !errors.Is(err, want) {
			t.Errorf("Comment = %v, want %v", err, want)
		}
		return nil
	}), newReq(), failWriter{ResponseRecorder: httptest.NewRecorder(), err: want})
}

func TestEverySendIsANoOpForHEAD(t *testing.T) {
	var (
		errs    map[string]error
		reached bool
	)

	r := newRouter()
	r.GET("/events", open(func(s *Writer) error {
		errs = map[string]error{
			"Send":          s.Send(Event{Data: "one"}),
			"SendData":      s.SendData("one"),
			"SendJSON":      s.SendJSON("x", 1),
			"SendComponent": s.SendComponent("x", comp("<p/>")),
			"Comment":       s.Comment("ping"),
		}
		reached = true
		return nil
	}))

	rec := do(r, http.MethodHead, "/events")
	if !reached {
		t.Fatal("the handler did not reach its end, so a send failed hard")
	}
	for name, err := range errs {
		if err != nil {
			t.Errorf("%s = %v, want no error", name, err)
		}
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}

func TestServeUnflushableWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	serveTo(func(c *tctx) error {
		return Serve(c, closedChan("one"), Text[string]("msg"))
	}, newReq(), noFlush{ResponseWriter: rec})

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestServeHeartbeatError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		want := errors.New("connection reset")
		got := make(chan error, 1)

		go func() {
			serveTo(func(c *tctx) error {
				err := Serve(c, make(chan string), Text[string]("msg"), Heartbeat(time.Second))
				got <- err
				return err
			}, newReq(), failWriter{ResponseRecorder: httptest.NewRecorder(), err: want})
		}()

		time.Sleep(2 * time.Second)
		synctest.Wait()

		select {
		case err := <-got:
			if !errors.Is(err, want) {
				t.Errorf("Serve = %v, want %v", err, want)
			}
		default:
			t.Error("the stream stayed open after the heartbeat failed")
		}
	})
}

type engineComponent interface {
	Render(ctx context.Context, w io.Writer) error
}

func card(s string) router.ComponentFunc { return comp("<li>" + s + "</li>") }

func engineCard(s string) engineComponent { return comp("<li>" + s + "</li>") }

func TestComponentOfATemplateEngine(t *testing.T) {
	rec := serve(func(c *tctx) error {
		return Serve(c, closedChan("one"), Component("row", engineCard))
	}, newReq())

	if got, want := rec.Body.String(), "event: row\ndata: <li>one</li>\n\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestRejectedEventKeepsTheStreamOpen(t *testing.T) {
	rec := serve(open(func(s *Writer) error {
		if err := s.Send(Event{ID: "7\nevent: forged", Data: "x"}); err == nil {
			t.Error("Send took an ID with a line break")
		}
		if err := s.SendJSON("bad", make(chan int)); err == nil {
			t.Error("SendJSON took a value that cannot be encoded")
		}
		if err := s.SendComponent("row", router.ComponentFunc(func(context.Context, io.Writer) error {
			return io.ErrUnexpectedEOF
		})); err == nil {
			t.Error("SendComponent took a component that fails")
		}
		if s.Closed() {
			t.Error("the writer closed a stream that nothing failed to reach")
		}
		return s.SendData("still open")
	}), newReq())

	if got, want := rec.Body.String(), "data: still open\n\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestDropsALargeFrameBuffer(t *testing.T) {
	var big, after int

	serve(open(func(s *Writer) error {
		if err := s.SendData(strings.Repeat("x", maxFrameBuf+1024)); err != nil {
			return err
		}
		big = s.buf.Cap()
		if err := s.SendData("small"); err != nil {
			return err
		}
		after = s.buf.Cap()
		return nil
	}), newReq())

	if big <= maxFrameBuf {
		t.Fatalf("the buffer of the large frame held %d bytes, want more than %d", big, maxFrameBuf)
	}
	if after > maxFrameBuf {
		t.Errorf("the buffer still holds %d bytes after a small frame, want it dropped", after)
	}
}

func TestNewStreamCopiesTheOptions(t *testing.T) {
	opts := []Option{Retry(time.Second)}
	stream := NewStream(Text[string]("msg"), opts...)
	opts[0] = Retry(9 * time.Second)

	rec := serve(func(c *tctx) error {
		return stream.Serve(c, closedChan("one"))
	}, newReq())

	want := "retry: 1000\n\nevent: msg\ndata: one\n\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestSendJSONTakesTheRouterOptions(t *testing.T) {
	type pair struct {
		A int `json:"a"`
		B int `json:"b"`
	}

	r := newRouter()
	r.JSONOptions(json.OmitZeroStructFields(true))
	r.GET("/events", open(func(s *Writer) error {
		if err := s.SendJSON("router", pair{A: 1}); err != nil {
			return err
		}
		return s.SendJSON("call", pair{A: 1}, json.OmitZeroStructFields(false))
	}))

	want := "event: router\ndata: {\"a\":1}\n\nevent: call\ndata: {\"a\":1,\"b\":0}\n\n"
	if got := do(r, http.MethodGet, "/events").Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// loopNoFlush unwraps to itself, as a buggy wrapper might, and cannot flush.
type loopNoFlush struct{ http.ResponseWriter }

func (w *loopNoFlush) Unwrap() http.ResponseWriter { return w }

func TestCanFlushStopsOnACyclicUnwrap(t *testing.T) {
	w := &loopNoFlush{ResponseWriter: httptest.NewRecorder()}

	done := make(chan bool, 1)
	go func() { done <- canFlush(w) }()

	select {
	case got := <-done:
		if got {
			t.Error("canFlush = true for a writer that cannot flush")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canFlush did not return for a cyclic Unwrap")
	}

	_, err := Open(router.NewBase(w, newReq()), http.StatusOK)
	if got := router.StatusOf(err); got != http.StatusInternalServerError {
		t.Errorf("status of the error = %d, want 500", got)
	}
}
