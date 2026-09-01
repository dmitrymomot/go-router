package router

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func sseReq() *http.Request { return httptest.NewRequest(http.MethodGet, "/events", nil) }

func sseServe(h HandlerFunc[*tctx], req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	sseServeTo(h, req, rec)
	return rec
}

func sseServeTo(h HandlerFunc[*tctx], req *http.Request, w http.ResponseWriter) {
	r := newTestRouter()
	r.GET("/events", h)
	r.ServeHTTP(w, req)
}

func sseOpen(fn func(s *SSEWriter) error, opts ...SSEOption) HandlerFunc[*tctx] {
	return func(c *tctx) error {
		s, err := c.SSE(http.StatusOK, opts...)
		if err != nil {
			return err
		}
		return fn(s)
	}
}

func TestSSEHeaders(t *testing.T) {
	rec := sseServe(sseOpen(func(*SSEWriter) error { return nil }), sseReq())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	for name, want := range map[string]string{
		HeaderContentType:     MIMETextEventStream,
		HeaderCacheControl:    "no-cache",
		HeaderXAccelBuffering: "no",
		HeaderConnection:      "keep-alive",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if got := rec.Header().Get(HeaderContentLength); got != "" {
		t.Errorf("Content-Length = %q, want none", got)
	}
	if !rec.Flushed {
		t.Error("the header did not reach the client")
	}
}

func TestSSEKeepsTheHandlerContentType(t *testing.T) {
	rec := sseServe(func(c *tctx) error {
		c.SetHeader(HeaderContentType, "text/event-stream; charset=utf-8")
		_, err := c.SSE(http.StatusOK)
		return err
	}, sseReq())

	if got := rec.Header().Get(HeaderContentType); got != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestSSENoConnectionHeaderOnHTTP2(t *testing.T) {
	req := sseReq()
	req.Proto, req.ProtoMajor, req.ProtoMinor = "HTTP/2.0", 2, 0

	rec := sseServe(sseOpen(func(*SSEWriter) error { return nil }), req)
	if got := rec.Header().Get(HeaderConnection); got != "" {
		t.Errorf("Connection = %q, want none", got)
	}
}

func TestSSESendWritesEveryField(t *testing.T) {
	rec := sseServe(sseOpen(func(s *SSEWriter) error {
		return s.Send(Event{ID: "7", Name: "tick", Data: "hello", Retry: 2 * time.Second})
	}), sseReq())

	want := "id: 7\nevent: tick\nretry: 2000\ndata: hello\n\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestSSESendData(t *testing.T) {
	rec := sseServe(sseOpen(func(s *SSEWriter) error { return s.SendData("hello") }), sseReq())

	if got, want := rec.Body.String(), "data: hello\n\n"; got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestSSEDataLines(t *testing.T) {
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
			rec := sseServe(sseOpen(func(s *SSEWriter) error {
				return s.Send(Event{Data: tt.data})
			}), sseReq())

			if got := rec.Body.String(); got != tt.want {
				t.Errorf("frame = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSSEComment(t *testing.T) {
	rec := sseServe(sseOpen(func(s *SSEWriter) error { return s.Comment("ping") }), sseReq())

	if got, want := rec.Body.String(), ": ping\n\n"; got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestSSECommentLines(t *testing.T) {
	rec := sseServe(sseOpen(func(s *SSEWriter) error { return s.Comment("one\ntwo") }), sseReq())

	if got, want := rec.Body.String(), ": one\n: two\n\n"; got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestSSESendJSON(t *testing.T) {
	rec := sseServe(sseOpen(func(s *SSEWriter) error {
		return s.SendJSON("user", map[string]string{"id": "7"})
	}), sseReq())

	if got, want := rec.Body.String(), "event: user\ndata: {\"id\":\"7\"}\n\n"; got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestSSESendJSONErrorWritesNothing(t *testing.T) {
	rec := sseServe(sseOpen(func(s *SSEWriter) error {
		if err := s.SendData("first"); err != nil {
			return err
		}
		err := s.SendJSON("bad", make(chan int))
		if err == nil {
			t.Error("SendJSON reported no error for a channel")
		}
		if got := StatusOf(err); got != http.StatusInternalServerError {
			t.Errorf("status of the error = %d, want 500", got)
		}
		return nil
	}), sseReq())

	if got, want := rec.Body.String(), "data: first\n\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestSSESendComponent(t *testing.T) {
	rec := sseServe(sseOpen(func(s *SSEWriter) error {
		return s.SendComponent("row", comp("<li>one</li>"))
	}), sseReq())

	if got, want := rec.Body.String(), "event: row\ndata: <li>one</li>\n\n"; got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestSSESendComponentSplitWrites(t *testing.T) {
	parts := []string{"<ul>\r", "\n<li>one</li>\n", "</ul>"}
	rec := sseServe(sseOpen(func(s *SSEWriter) error {
		return s.SendComponent("list", ComponentFunc(func(_ context.Context, w io.Writer) error {
			for _, p := range parts {
				if _, err := w.Write([]byte(p)); err != nil {
					return err
				}
			}
			return nil
		}))
	}), sseReq())

	want := "event: list\ndata: <ul>\ndata: <li>one</li>\ndata: </ul>\n\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestSSESendComponentPassesTheContext(t *testing.T) {
	r := newTestRouter()
	r.GET("/u/{id}/events", sseOpen(func(s *SSEWriter) error {
		return s.SendComponent("row", ComponentFunc(func(ctx context.Context, w io.Writer) error {
			b, ok := FromContext(ctx)
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

func TestSSESendComponentErrorWritesNothing(t *testing.T) {
	rec := sseServe(sseOpen(func(s *SSEWriter) error {
		if err := s.SendData("first"); err != nil {
			return err
		}
		return s.SendComponent("row", ComponentFunc(func(_ context.Context, w io.Writer) error {
			if _, err := io.WriteString(w, "<li>partial"); err != nil {
				return err
			}
			return io.ErrUnexpectedEOF
		}))
	}), sseReq())

	if strings.Contains(rec.Body.String(), "partial") {
		t.Errorf("the body leaked the partial frame: %q", rec.Body.String())
	}
	if got, want := rec.Body.String(), "data: first\n\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestSSESendComponentKeepsAnHTTPError(t *testing.T) {
	var got error
	sseServe(sseOpen(func(s *SSEWriter) error {
		got = s.SendComponent("row", ComponentFunc(func(context.Context, io.Writer) error {
			return ErrNotFound
		}))
		return nil
	}), sseReq())

	if !errors.Is(got, ErrNotFound) {
		t.Errorf("error = %v, want the 404 of the component", got)
	}
}

func TestSSEFieldWithALineBreak(t *testing.T) {
	for _, e := range []Event{
		{ID: "7\nevent: forged", Data: "x"},
		{Name: "tick\ndata: forged", Data: "x"},
		{ID: "7\rx", Data: "x"},
	} {
		rec := sseServe(sseOpen(func(s *SSEWriter) error {
			err := s.Send(e)
			if err == nil {
				t.Errorf("Send(%+v) reported no error", e)
			}
			if got := StatusOf(err); got != http.StatusInternalServerError {
				t.Errorf("status of the error = %d, want 500", got)
			}
			return nil
		}), sseReq())

		if rec.Body.Len() != 0 {
			t.Errorf("body = %q, want empty", rec.Body.String())
		}
	}
}

func TestSSEEventIDWithNull(t *testing.T) {
	for _, id := range []string{"\x00", "7\x00x"} {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			rec := sseServe(sseOpen(func(s *SSEWriter) error {
				err := s.Send(Event{ID: id, Data: "x"})
				if err == nil {
					t.Errorf("Send took ID %q", id)
				}
				if got := StatusOf(err); got != http.StatusInternalServerError {
					t.Errorf("status of the error = %d, want 500", got)
				}
				return s.SendData("still open")
			}), sseReq())

			if got, want := rec.Body.String(), "data: still open\n\n"; got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
		})
	}
}

func TestSSESendAfterAFailure(t *testing.T) {
	want := errors.New("connection reset")
	var first, second error

	sseServeTo(sseOpen(func(s *SSEWriter) error {
		first, second = s.Send(Event{Data: "one"}), s.Send(Event{Data: "two"})
		if !s.Closed() {
			t.Error("the writer reports the stream as open after a failed send")
		}
		return nil
	}), sseReq(), failWriter{ResponseRecorder: httptest.NewRecorder(), err: want})

	if !errors.Is(first, want) {
		t.Errorf("first send = %v, want %v", first, want)
	}
	if !errors.Is(second, want) {
		t.Errorf("second send = %v, want %v", second, want)
	}
}

func TestSSEHEAD(t *testing.T) {
	var (
		open             bool
		sendErr, commErr error
		reached          bool
	)

	r := newTestRouter()
	r.GET("/events", sseOpen(func(s *SSEWriter) error {
		open = !s.Closed()
		sendErr = s.Send(Event{Data: "hello"})
		commErr = s.Comment("ping")
		reached = true
		return nil
	}, SSERetry(time.Second)))

	rec := do(r, http.MethodHead, "/events")
	if !reached {
		t.Fatal("the handler did not reach its end, so a send failed hard")
	}
	if open {
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
	if got := rec.Header().Get(HeaderContentType); got != MIMETextEventStream {
		t.Errorf("Content-Type = %q", got)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}

func TestSSEUnflushableWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	sseServeTo(sseOpen(func(*SSEWriter) error {
		t.Error("SSE returned a writer for a response writer that cannot flush")
		return nil
	}), sseReq(), noFlush{ResponseWriter: rec})

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestSSEFlushThroughAWrapper(t *testing.T) {
	rec := httptest.NewRecorder()
	sseServeTo(sseOpen(func(s *SSEWriter) error {
		return s.SendData("hello")
	}), sseReq(), unwrapWriter{ResponseWriter: rec})

	if got, want := rec.Body.String(), "data: hello\n\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if !rec.Flushed {
		t.Error("the event did not reach the client")
	}
}

func TestSSERetryOption(t *testing.T) {
	rec := sseServe(sseOpen(func(s *SSEWriter) error {
		return s.SendData("hello")
	}, SSERetry(3*time.Second)), sseReq())

	if got, want := rec.Body.String(), "retry: 3000\n\ndata: hello\n\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestSSERetryBelowAMillisecond(t *testing.T) {
	rec := sseServe(sseOpen(func(s *SSEWriter) error {
		return s.SendData("hello")
	}, SSERetry(time.Microsecond)), sseReq())

	if got, want := rec.Body.String(), "data: hello\n\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestLastEventID(t *testing.T) {
	req := sseReq()
	req.Header.Set(HeaderLastEventID, "42")

	rec := sseServe(func(c *tctx) error {
		s, err := c.SSE(http.StatusOK)
		if err != nil {
			return err
		}
		if got := c.LastEventID(); got != "42" {
			t.Errorf("Base.LastEventID = %q, want %q", got, "42")
		}
		if got := s.LastEventID(); got != "42" {
			t.Errorf("SSEWriter.LastEventID = %q, want %q", got, "42")
		}
		if got := s.Request(); got != c.Request() {
			t.Error("SSEWriter.Request returned another request")
		}
		return nil
	}, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestLastEventIDIsEmptyOnAFirstConnection(t *testing.T) {
	sseServe(func(c *tctx) error {
		if got := c.LastEventID(); got != "" {
			t.Errorf("LastEventID = %q, want empty", got)
		}
		return c.NoContent(http.StatusOK)
	}, sseReq())
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

func TestServeSSEDrainsTheChannel(t *testing.T) {
	rec := sseServe(func(c *tctx) error {
		return ServeSSE(c, closedChan("one", "two"), SSEText[string]("msg"))
	}, sseReq())

	want := "event: msg\ndata: one\n\nevent: msg\ndata: two\n\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestServeSSEReportsNoErrorAtTheEnd(t *testing.T) {
	var got error
	sseServe(func(c *tctx) error {
		got = ServeSSE(c, closedChan[string](), SSEText[string]("msg"))
		return got
	}, sseReq())

	if got != nil {
		t.Errorf("ServeSSE = %v, want nil", got)
	}
}

func TestServeSSEStopsWhenTheClientGoesAway(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var got error
	rec := sseServe(func(c *tctx) error {
		got = ServeSSE(c, make(chan string), SSEText[string]("msg"))
		return got
	}, sseReq().WithContext(ctx))

	if got != nil {
		t.Errorf("ServeSSE = %v, want nil", got)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestServeSSEHeartbeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ch := make(chan string)
		done := make(chan struct{})
		rec := httptest.NewRecorder()

		go func() {
			defer close(done)
			sseServeTo(func(c *tctx) error {
				return ServeSSE(c, ch, SSEText[string]("msg"), SSEHeartbeat(time.Second))
			}, sseReq(), rec)
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

func TestServeSSENoHeartbeatByDefault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ch := make(chan string)
		done := make(chan struct{})
		rec := httptest.NewRecorder()

		go func() {
			defer close(done)
			sseServeTo(func(c *tctx) error {
				return ServeSSE(c, ch, SSEText[string]("msg"))
			}, sseReq(), rec)
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

func TestServeSSECloseOption(t *testing.T) {
	rec := sseServe(func(c *tctx) error {
		return ServeSSE(c, closedChan("one"), SSEText[string]("msg"),
			SSEClose(Event{Name: "close", Data: "done"}))
	}, sseReq())

	want := "event: msg\ndata: one\n\nevent: close\ndata: done\n\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestServeSSECloseOptionSkippedOnDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	rec := sseServe(func(c *tctx) error {
		return ServeSSE(c, make(chan string), SSEText[string]("msg"),
			SSEClose(Event{Name: "close"}))
	}, sseReq().WithContext(ctx))

	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}

func TestServeSSESenderError(t *testing.T) {
	want := errors.New("render failed")
	var got error

	rec := sseServe(func(c *tctx) error {
		got = ServeSSE(c, closedChan("one"), func(*SSEWriter, string) error { return want })
		return got
	}, sseReq())

	if !errors.Is(got, want) {
		t.Errorf("ServeSSE = %v, want %v", got, want)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want the 200 of the committed stream", rec.Code)
	}
}

func TestServeSSEWithoutASender(t *testing.T) {
	rec := sseServe(func(c *tctx) error {
		return ServeSSE(c, closedChan("one"), nil)
	}, sseReq())

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestServeSSEHEAD(t *testing.T) {
	ch := make(chan string, 1)
	ch <- "one"

	r := newTestRouter()
	r.GET("/events", func(c *tctx) error { return ServeSSE(c, ch, SSEText[string]("msg")) })

	rec := do(r, http.MethodHead, "/events")
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	if len(ch) != 1 {
		t.Error("the stream read a value for a HEAD request")
	}
}

func TestSSESenders(t *testing.T) {
	t.Run("JSON", func(t *testing.T) {
		rec := sseServe(func(c *tctx) error {
			return ServeSSE(c, closedChan(map[string]int{"n": 1}), SSEJSON[map[string]int]("count"))
		}, sseReq())

		if got, want := rec.Body.String(), "event: count\ndata: {\"n\":1}\n\n"; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})

	t.Run("text of a Stringer", func(t *testing.T) {
		rec := sseServe(func(c *tctx) error {
			return ServeSSE(c, closedChan(stamp{n: 4}), SSEText[stamp](""))
		}, sseReq())

		if got, want := rec.Body.String(), "data: n=4\n\n"; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})

	t.Run("component", func(t *testing.T) {
		rec := sseServe(func(c *tctx) error {
			return ServeSSE(c, closedChan("one"), SSEComponent("row", card))
		}, sseReq())

		if got, want := rec.Body.String(), "event: row\ndata: <li>one</li>\n\n"; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})

	t.Run("events", func(t *testing.T) {
		rec := sseServe(func(c *tctx) error {
			return ServeSSE(c, closedChan(Event{ID: "1", Name: "tick", Data: "one"}), SSEEvents())
		}, sseReq())

		if got, want := rec.Body.String(), "id: 1\nevent: tick\ndata: one\n\n"; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})
}

func TestSSEStreamServe(t *testing.T) {
	stream := NewSSEStream(SSEComponent("row", card), SSERetry(time.Second))

	for _, item := range []string{"one", "two"} {
		rec := sseServe(func(c *tctx) error {
			return stream.Serve(c, closedChan(item))
		}, sseReq())

		want := "retry: 1000\n\nevent: row\ndata: <li>" + item + "</li>\n\n"
		if got := rec.Body.String(); got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	}
}

func TestNewSSEStreamWithoutASender(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewSSEStream took a nil sender")
		}
	}()
	NewSSEStream[string](nil)
}

func TestSSERejectsANilOptionBeforeCommitting(t *testing.T) {
	rec := httptest.NewRecorder()
	b := NewBase(rec, sseReq())

	_, err := b.SSE(http.StatusOK, nil)
	if cause := errors.Unwrap(err); cause == nil || cause.Error() != nilSSEOptionError {
		t.Errorf("SSE cause = %v, want %q", cause, nilSSEOptionError)
	}
	if b.Response().Committed {
		t.Error("SSE committed the response before rejecting the option")
	}
}

func TestServeSSERejectsANilOption(t *testing.T) {
	var got error
	sseServe(func(c *tctx) error {
		got = ServeSSE(c, closedChan("one"), SSEText[string]("msg"), nil)
		return got
	}, sseReq())

	if cause := errors.Unwrap(got); cause == nil || cause.Error() != nilSSEOptionError {
		t.Errorf("ServeSSE cause = %v, want %q", cause, nilSSEOptionError)
	}
}

func TestNewSSEStreamRejectsANilOption(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewSSEStream took a nil option")
		}
	}()
	NewSSEStream(SSEText[string]("msg"), nil)
}

func TestSSEComponentNeedsAView(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("SSEComponent took a nil view")
		}
	}()
	_ = SSEComponent[string, Component]("row", nil)
}

func TestSSEOverAServer(t *testing.T) {
	ch := make(chan string)
	handlerErr := make(chan error, 1)

	r := newTestRouter()
	r.GET("/events", func(c *tctx) error {
		err := ServeSSE(c, ch, SSEText[string]("msg"), SSERetry(2*time.Second))
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

	if got := res.Header.Get(HeaderContentType); got != MIMETextEventStream {
		t.Errorf("Content-Type = %q, want %q", got, MIMETextEventStream)
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

func TestSSEFlushError(t *testing.T) {
	want := errors.New("stream reset")
	var opened, sent error

	sseServeTo(func(c *tctx) error {
		s, err := c.SSE(http.StatusOK)
		opened = err
		if err != nil {
			return nil
		}
		sent = s.Send(Event{Data: "one"})
		return nil
	}, sseReq(), flushErrWriter{ResponseRecorder: httptest.NewRecorder(), err: want})

	if !errors.Is(opened, want) {
		t.Errorf("SSE = %v, want %v", opened, want)
	}
	if sent != nil {
		t.Errorf("Send = %v, want no send at all", sent)
	}
}

func TestSSEWriteDeadlineError(t *testing.T) {
	rec := httptest.NewRecorder()
	sseServeTo(sseOpen(func(*SSEWriter) error {
		t.Error("SSE returned a writer although the deadline stayed")
		return nil
	}), sseReq(), deadlineWriter{ResponseRecorder: rec, err: errors.New("no")})

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestSSERetryWriteError(t *testing.T) {
	want := errors.New("connection reset")
	var opened error

	sseServeTo(func(c *tctx) error {
		_, opened = c.SSE(http.StatusOK, SSERetry(time.Second))
		return nil
	}, sseReq(), failWriter{ResponseRecorder: httptest.NewRecorder(), err: want})

	if !errors.Is(opened, want) {
		t.Errorf("SSE = %v, want %v", opened, want)
	}
}

func TestSSEEverySendReportsTheFailure(t *testing.T) {
	want := errors.New("connection reset")

	sseServeTo(sseOpen(func(s *SSEWriter) error {
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
	}), sseReq(), failWriter{ResponseRecorder: httptest.NewRecorder(), err: want})
}

func TestSSEEverySendIsANoOpForHEAD(t *testing.T) {
	var (
		errs    map[string]error
		reached bool
	)

	r := newTestRouter()
	r.GET("/events", sseOpen(func(s *SSEWriter) error {
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

func TestServeSSEUnflushableWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	sseServeTo(func(c *tctx) error {
		return ServeSSE(c, closedChan("one"), SSEText[string]("msg"))
	}, sseReq(), noFlush{ResponseWriter: rec})

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestServeSSEHeartbeatError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		want := errors.New("connection reset")
		got := make(chan error, 1)

		go func() {
			sseServeTo(func(c *tctx) error {
				err := ServeSSE(c, make(chan string), SSEText[string]("msg"), SSEHeartbeat(time.Second))
				got <- err
				return err
			}, sseReq(), failWriter{ResponseRecorder: httptest.NewRecorder(), err: want})
		}()

		time.Sleep(2 * time.Second)
		synctest.Wait()

		select {
		case err := <-got:
			if !errors.Is(err, want) {
				t.Errorf("ServeSSE = %v, want %v", err, want)
			}
		default:
			t.Error("the stream stayed open after the heartbeat failed")
		}
	})
}

type engineComponent interface {
	Render(ctx context.Context, w io.Writer) error
}

func card(s string) ComponentFunc { return comp("<li>" + s + "</li>") }

func engineCard(s string) engineComponent { return comp("<li>" + s + "</li>") }

func TestSSEComponentOfATemplateEngine(t *testing.T) {
	rec := sseServe(func(c *tctx) error {
		return ServeSSE(c, closedChan("one"), SSEComponent("row", engineCard))
	}, sseReq())

	if got, want := rec.Body.String(), "event: row\ndata: <li>one</li>\n\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestSSERejectedEventKeepsTheStreamOpen(t *testing.T) {
	rec := sseServe(sseOpen(func(s *SSEWriter) error {
		if err := s.Send(Event{ID: "7\nevent: forged", Data: "x"}); err == nil {
			t.Error("Send took an ID with a line break")
		}
		if err := s.SendJSON("bad", make(chan int)); err == nil {
			t.Error("SendJSON took a value that cannot be encoded")
		}
		if err := s.SendComponent("row", ComponentFunc(func(context.Context, io.Writer) error {
			return io.ErrUnexpectedEOF
		})); err == nil {
			t.Error("SendComponent took a component that fails")
		}
		if s.Closed() {
			t.Error("the writer closed a stream that nothing failed to reach")
		}
		return s.SendData("still open")
	}), sseReq())

	if got, want := rec.Body.String(), "data: still open\n\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestSSEDropsALargeFrameBuffer(t *testing.T) {
	var big, after int

	sseServe(sseOpen(func(s *SSEWriter) error {
		if err := s.SendData(strings.Repeat("x", maxPooledRenderBuf+1024)); err != nil {
			return err
		}
		big = s.buf.Cap()
		if err := s.SendData("small"); err != nil {
			return err
		}
		after = s.buf.Cap()
		return nil
	}), sseReq())

	if big <= maxPooledRenderBuf {
		t.Fatalf("the buffer of the large frame held %d bytes, want more than %d", big, maxPooledRenderBuf)
	}
	if after > maxPooledRenderBuf {
		t.Errorf("the buffer still holds %d bytes after a small frame, want it dropped", after)
	}
}

func TestNewSSEStreamCopiesTheOptions(t *testing.T) {
	opts := []SSEOption{SSERetry(time.Second)}
	stream := NewSSEStream(SSEText[string]("msg"), opts...)
	opts[0] = SSERetry(9 * time.Second)

	rec := sseServe(func(c *tctx) error {
		return stream.Serve(c, closedChan("one"))
	}, sseReq())

	want := "retry: 1000\n\nevent: msg\ndata: one\n\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}
