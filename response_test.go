package router

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

var (
	_ http.Hijacker = (*Response)(nil)
	_ http.Flusher  = (*Response)(nil)
)

type statusWriter struct {
	http.ResponseWriter
	codes []int
}

func (w *statusWriter) WriteHeader(code int) {
	w.codes = append(w.codes, code)
	w.ResponseWriter.WriteHeader(code)
}

type recordSink struct {
	slog.Handler
	records []slog.Record
}

func (h *recordSink) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordSink) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}

func intAttr(r slog.Record, key string) (int64, bool) {
	var (
		v  int64
		ok bool
	)
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			v, ok = a.Value.Int64(), true
			return false
		}
		return true
	})
	return v, ok
}

func captureLogs(t *testing.T) *recordSink {
	t.Helper()
	sink := &recordSink{Handler: slog.Default().Handler()}
	old := slog.Default()
	slog.SetDefault(slog.New(sink))
	t.Cleanup(func() { slog.SetDefault(old) })
	return sink
}

func TestBeforeHookSeesTheStatusThatGoesOut(t *testing.T) {
	w := &statusWriter{ResponseWriter: httptest.NewRecorder()}
	res := &Response{ResponseWriter: w}

	var seen []int
	res.Before(func() {
		seen = append(seen, res.Status)
		res.Header().Set("X-Status", fmt.Sprint(res.Status))
	})
	res.WriteHeader(http.StatusTeapot)

	if len(seen) != 1 || seen[0] != http.StatusTeapot {
		t.Errorf("the hook saw %v, want [418]; a hook that reads the status runs after it is set", seen)
	}
	if got := res.Header().Get("X-Status"); got != "418" {
		t.Errorf("X-Status = %q, want %q", got, "418")
	}
	if len(w.codes) != 1 || w.codes[0] != http.StatusTeapot {
		t.Errorf("the writer saw %v, want [418]", w.codes)
	}
}

func TestWriteHeaderDropsASecondStatusAndLogsIt(t *testing.T) {
	sink := captureLogs(t)

	w := &statusWriter{ResponseWriter: httptest.NewRecorder()}
	res := &Response{ResponseWriter: w}
	hooks := 0
	res.Before(func() { hooks++ })

	res.WriteHeader(http.StatusOK)
	res.WriteHeader(http.StatusInternalServerError)

	if res.Status != http.StatusOK {
		t.Errorf("Status = %d, want 200; the second call must not replace it", res.Status)
	}
	if len(w.codes) != 1 {
		t.Errorf("the writer saw %v, want one status", w.codes)
	}
	if hooks != 1 {
		t.Errorf("the hooks ran %d times, want 1", hooks)
	}
	if len(sink.records) != 1 {
		t.Fatalf("logged %d records, want 1 that names the dropped status", len(sink.records))
	}
	if sink.records[0].Level != slog.LevelDebug {
		t.Errorf("level = %v, want debug", sink.records[0].Level)
	}
	if got, ok := intAttr(sink.records[0], "dropped"); !ok || got != http.StatusInternalServerError {
		t.Errorf("dropped = %d/%v, want 500", got, ok)
	}
	if got, ok := intAttr(sink.records[0], "status"); !ok || got != http.StatusOK {
		t.Errorf("status = %d/%v, want 200", got, ok)
	}
}

func TestWriteHeaderKeepsAnInformationalStatusOutOfTheAnswer(t *testing.T) {
	type answer struct {
		status    int
		committed bool
		hooks     int
	}
	got := make(chan answer, 1)

	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		res := c.Response()
		hooks := 0
		res.Before(func() { hooks++ })

		res.Header().Set("Link", "</app.css>; rel=preload")
		res.WriteHeader(http.StatusEarlyHints)
		if res.Committed || res.Status != 0 {
			return ErrInternalServerError.WithMessage("103 committed the response")
		}
		err := c.String(http.StatusOK, "page")
		got <- answer{res.Status, res.Committed, hooks}
		return err
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	var hints []string
	trace := &httptrace.ClientTrace{
		Got1xxResponse: func(code int, h textproto.MIMEHeader) error {
			hints = append(hints, fmt.Sprintf("%d %s", code, h.Get("Link")))
			return nil
		},
	}
	req, err := http.NewRequestWithContext(
		httptrace.WithClientTrace(t.Context(), trace), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer res.Body.Close() //nolint:errcheck // The test is done with it.

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if want := []string{"103 </app.css>; rel=preload"}; len(hints) != 1 || hints[0] != want[0] {
		t.Errorf("informational responses = %v, want %v", hints, want)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200; the 103 swallowed the final status", res.StatusCode)
	}
	if string(body) != "page" {
		t.Errorf("body = %q, want %q", body, "page")
	}

	a := <-got
	if a.status != http.StatusOK || !a.committed {
		t.Errorf("Status/Committed = %d/%v, want 200/true", a.status, a.committed)
	}
	if a.hooks != 1 {
		t.Errorf("the hooks ran %d times, want 1; a 103 runs none of them", a.hooks)
	}
}

func TestHijackTakesOverTheConnection(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		hj, ok := c.ResponseWriter().(http.Hijacker)
		if !ok {
			return ErrInternalServerError.WithMessage("the response writer is no http.Hijacker")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return ErrInternalServerError.WithError(err)
		}
		defer conn.Close() //nolint:errcheck // The connection is going away.
		//nolint:errcheck // The assertions below report what arrived.
		buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\n\r\nhi")
		return buf.Flush()
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close() //nolint:errcheck // The test is done with it.
	if _, err := fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	out, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(out), "101 Switching Protocols") || !strings.HasSuffix(string(out), "hi") {
		t.Errorf("answer = %q, want the hijacked one", out)
	}
}

func TestHijackReportsAWriterThatCannotDoIt(t *testing.T) {
	res := &Response{ResponseWriter: httptest.NewRecorder()}
	conn, buf, err := res.Hijack()
	if conn != nil || buf != nil {
		t.Errorf("Hijack returned %v/%v, want nothing", conn, buf)
	}
	if err == nil {
		t.Fatal("Hijack found a connection under a recorder")
	}
}

type wrapWriter struct {
	http.ResponseWriter
}

func (w *wrapWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type loopWriter struct {
	http.ResponseWriter
}

func (w *loopWriter) Unwrap() http.ResponseWriter { return w }

func TestUnwrapResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	res := &Response{ResponseWriter: rec}

	tests := []struct {
		name string
		w    http.ResponseWriter
		want bool
	}{
		{"the response itself", res, true},
		{"one wrapper", &wrapWriter{res}, true},
		{"three wrappers", &wrapWriter{&wrapWriter{&wrapWriter{res}}}, true},
		{"a plain writer", rec, false},
		{"a wrapper over a plain writer", &wrapWriter{rec}, false},
		{"a cycle", &loopWriter{rec}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := UnwrapResponse(tc.w)
			if ok != tc.want {
				t.Fatalf("UnwrapResponse ok = %v, want %v", ok, tc.want)
			}
			if ok && got != res {
				t.Errorf("UnwrapResponse returned another response")
			}
			if !ok && got != nil {
				t.Errorf("UnwrapResponse returned %v with ok false", got)
			}
		})
	}
}

func TestUnwrapResponseReadsTheStatusAndTheSize(t *testing.T) {
	res := &Response{ResponseWriter: httptest.NewRecorder()}
	res.WriteHeader(http.StatusCreated)
	//nolint:errcheck // The recorder never fails.
	res.WriteString("body")

	var w http.ResponseWriter = &wrapWriter{res}
	got, ok := UnwrapResponse(w)
	if !ok {
		t.Fatal("UnwrapResponse found no response")
	}
	if got.Status != http.StatusCreated || got.Size != 4 {
		t.Errorf("Status/Size = %d/%d, want 201/4", got.Status, got.Size)
	}
}

func TestWriteHeaderCommitsASwitchingProtocols(t *testing.T) {
	rec := httptest.NewRecorder()
	res := &Response{ResponseWriter: rec}
	hooks := 0
	res.Before(func() { hooks++ })

	res.WriteHeader(http.StatusSwitchingProtocols)

	if res.Status != http.StatusSwitchingProtocols || !res.Committed {
		t.Errorf("Status/Committed = %d/%v, want 101/true", res.Status, res.Committed)
	}
	if hooks != 1 {
		t.Errorf("the hooks ran %d times, want 1", hooks)
	}
	if rec.Code != http.StatusSwitchingProtocols {
		t.Errorf("the client saw %d, want 101", rec.Code)
	}
}

func TestObserveReportsAnUpgradeAsAnUpgrade(t *testing.T) {
	r := newTestRouter()
	got := 0
	r.Observe(func(_ Context, status int, _ int64, _ time.Duration, _ error) { got = status })
	r.GET("/ws", func(c *tctx) error {
		c.Response().WriteHeader(http.StatusSwitchingProtocols)
		return nil
	})

	do(r, http.MethodGet, "/ws")

	if got != http.StatusSwitchingProtocols {
		t.Errorf("the observer saw %d, want 101", got)
	}
}

func TestFlushCommitsTheResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	res := &Response{ResponseWriter: rec}
	ran := false
	res.Before(func() { ran = true })

	res.Flush()

	if res.Status != http.StatusOK || !res.Committed || !ran {
		t.Errorf("after Flush: Status = %d, Committed = %v, hook ran = %v; want 200, true, true",
			res.Status, res.Committed, ran)
	}
}

type readFromWriter struct {
	http.ResponseWriter
	used bool
}

func (w *readFromWriter) ReadFrom(r io.Reader) (int64, error) {
	w.used = true
	return io.Copy(w.ResponseWriter, r)
}

func TestReadFromReachesTheWriterUnderneath(t *testing.T) {
	rec := httptest.NewRecorder()
	src := func() io.Reader { return io.LimitReader(strings.NewReader("hello"), 5) }

	under := &readFromWriter{ResponseWriter: rec}
	res := &Response{ResponseWriter: under}
	n, err := io.Copy(res, src())
	if err != nil || n != 5 || !under.used {
		t.Fatalf("copy = %d, %v, delegated = %v; want 5, nil, true", n, err, under.used)
	}
	if res.Size != 5 || rec.Body.String() != "hello" {
		t.Errorf("Size = %d, body = %q; want 5, %q", res.Size, rec.Body, "hello")
	}

	plain := httptest.NewRecorder()
	res = &Response{ResponseWriter: plain}
	if _, err := io.Copy(res, src()); err != nil || res.Size != 5 {
		t.Errorf("fallback: Size = %d, err = %v; want 5, nil", res.Size, err)
	}
}

func TestResponseBeforeRejectsNil(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want panic")
		}
	}()
	new(Response).Before(nil)
}

// After a hijack the caller owns the wire, so the router must not write on it
// again or report a status the connection never carried.
func TestHijackCommitsTheResponse(t *testing.T) {
	var (
		status   int
		observed = make(chan int, 1)
	)
	r := newTestRouter()
	r.Observe(func(_ Context, code int, _ int64, _ time.Duration, _ error) { observed <- code })
	r.GET("/hj", func(c *tctx) error {
		conn, bw, err := c.Response().Hijack()
		if err != nil {
			return err
		}
		status = c.Response().Status
		if !c.Response().Committed {
			t.Error("the response is not committed after a hijack")
		}
		_, _ = bw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: x\r\n\r\n")
		_ = bw.Flush()
		_ = conn.Close()
		// An error after the hijack must not reach the wire again.
		return ErrInternalServerError
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("GET /hj HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	if !strings.Contains(line, "101") {
		t.Errorf("status line = %q, want the 101 the handler wrote", line)
	}
	if status != http.StatusSwitchingProtocols {
		t.Errorf("Response.Status after a hijack = %d, want 101", status)
	}
	select {
	case got := <-observed:
		if got != http.StatusSwitchingProtocols {
			t.Errorf("observed status = %d, want 101", got)
		}
	case <-time.After(time.Second):
		t.Error("Observe never ran")
	}
}

func TestCaptureRecordsTheAnswerAndPassesItOn(t *testing.T) {
	rec := httptest.NewRecorder()
	res := &Response{ResponseWriter: rec}
	res.Before(func() { res.Header().Set("X-Hook", "ran") })

	stop := res.Capture(64)
	res.Header().Set(HeaderContentType, MIMETextPlain)
	if _, err := res.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := res.WriteString("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := res.ReadFrom(strings.NewReader("c")); err != nil {
		t.Fatal(err)
	}
	got := stop()
	res.Header().Set("X-Late", "1")

	if got.Status != http.StatusOK || string(got.Body) != "abc" || got.Truncated {
		t.Errorf("recorded %d %q truncated=%v, want 200 %q false", got.Status, got.Body, got.Truncated, "abc")
	}
	if got.Header.Get("X-Hook") != "ran" || got.Header.Get(HeaderContentType) != MIMETextPlain {
		t.Errorf("recorded header = %v, want the hook's and the handler's", got.Header)
	}
	if got.Header.Get("X-Late") != "" {
		t.Error("the recorded header changed after the status went out")
	}
	if rec.Body.String() != "abc" || res.Size != 3 {
		t.Errorf("client got %q, Size = %d; want %q, 3", rec.Body, res.Size, "abc")
	}
}

func TestCaptureStopsAtTheLimit(t *testing.T) {
	tests := []struct {
		name      string
		writes    []string
		want      string
		limit     int
		truncated bool
	}{
		{name: "over", limit: 4, writes: []string{"hello"}, want: "hell", truncated: true},
		{name: "over across writes", limit: 4, writes: []string{"he", "llo"}, want: "hell", truncated: true},
		{name: "zero", limit: 0, writes: []string{"hello"}, want: "", truncated: true},
		{name: "exactly at", limit: 5, writes: []string{"hello"}, want: "hello"},
		{name: "nothing written", limit: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			res := &Response{ResponseWriter: rec}
			stop := res.Capture(tt.limit)
			for _, s := range tt.writes {
				if _, err := res.WriteString(s); err != nil {
					t.Fatal(err)
				}
			}
			got := stop()
			if string(got.Body) != tt.want || got.Truncated != tt.truncated {
				t.Errorf("recorded %q truncated=%v, want %q %v", got.Body, got.Truncated, tt.want, tt.truncated)
			}
			if rec.Body.String() != strings.Join(tt.writes, "") {
				t.Errorf("client got %q, want all of it", rec.Body)
			}
		})
	}
}

func TestCaptureKeepsFlushHijackAndDeadlines(t *testing.T) {
	rec := httptest.NewRecorder()
	res := &Response{ResponseWriter: rec}
	stop := res.Capture(8)
	res.Flush()
	if !rec.Flushed {
		t.Error("Flush did not reach the recorder")
	}
	if got := stop(); got.Status != http.StatusOK {
		t.Errorf("recorded status = %d after a Flush, want 200", got.Status)
	}

	type hijacked struct {
		status    int
		committed bool
	}
	seen := make(chan hijacked, 1)
	r := newTestRouter()
	r.GET("/deadline", func(c *tctx) error {
		stop := c.Response().Capture(8)
		defer stop()
		if err := http.NewResponseController(c.Response()).SetWriteDeadline(time.Now().Add(time.Minute)); err != nil {
			return ErrInternalServerError.WithError(err)
		}
		return c.String(http.StatusOK, "ok")
	})
	r.GET("/hijack", func(c *tctx) error {
		stop := c.Response().Capture(8)
		conn, buf, err := c.Response().Hijack()
		if err != nil {
			return ErrInternalServerError.WithError(err)
		}
		defer conn.Close() //nolint:errcheck // The connection is going away.
		//nolint:errcheck // The client below reports what arrived.
		buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\n\r\n")
		seen <- hijacked{status: stop().Status, committed: c.Response().Committed}
		return buf.Flush()
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/deadline")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck,gosec // Only the status matters.
	if resp.StatusCode != http.StatusOK {
		t.Errorf("deadline: status = %d, want 200", resp.StatusCode)
	}

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint:errcheck // The test is done with it.
	if _, err := fmt.Fprint(conn, "GET /hijack HTTP/1.1\r\nHost: example.com\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-seen:
		if got.status != 0 || !got.committed {
			t.Errorf("after a hijack: recorded %d, committed %v; want 0, true", got.status, got.committed)
		}
	case <-time.After(5 * time.Second):
		t.Error("the handler never hijacked the connection")
	}
}

// MaxBytesReader closes the connection only when it reaches the writer of
// net/http, so a capture left in the chain must not hide it.
func TestCaptureKeepsA413ClosingTheConnection(t *testing.T) {
	r := newTestRouter()
	r.MaxBodyBytes(16)
	r.POST("/b", func(c *tctx) error {
		c.Response().Capture(64)
		_, err := c.Bind[map[string]any]()
		return err
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	body := `{"k":"` + strings.Repeat("a", 4096) + `"}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/b", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(HeaderContentType, MIMEApplicationJSON)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // The test is done with it.

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
	if !resp.Close {
		t.Error("the server kept the connection open after a 413")
	}
}

func TestCaptureRecordsTheErrorHandlerWhenStoppedAfterIt(t *testing.T) {
	captureLogs(t)
	var got Recorded
	r := newTestRouter()
	r.Use(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			stop := c.Response().Capture(64)
			HandleError(c, next(c))
			got = stop()
			return nil
		}
	})
	r.GET("/", func(*tctx) error { return ErrNotFound })

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got.Status != http.StatusNotFound || string(got.Body) != rec.Body.String() {
		t.Errorf("recorded %d %q, want 404 %q", got.Status, got.Body, rec.Body)
	}
}

// swapWriter stands in for Gzip: it puts its own writer in and takes it out on
// the way back, whatever sits in its place by then.
func swapWriter(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
	return func(c *tctx) error {
		res := c.Response()
		w := &wrapWriter{ResponseWriter: res.ResponseWriter}
		res.ResponseWriter = w
		defer func() { res.ResponseWriter = w.ResponseWriter }()
		return next(c)
	}
}

func TestCaptureEndsWhenAnOuterWriterIsPutBack(t *testing.T) {
	captureLogs(t)
	var stop func() Recorded
	r := newTestRouter()
	r.Use(swapWriter)
	r.GET("/", func(c *tctx) error {
		stop = c.Response().Capture(64)
		return ErrConflict
	})

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if got := stop(); got.Status != 0 || len(got.Body) != 0 {
		t.Errorf("recorded %d %q after the outer writer was put back, want nothing", got.Status, got.Body)
	}
}

// redirectToOK stands in for HTMXRedirect: it turns a redirect into a 200.
type redirectToOK struct {
	http.ResponseWriter
}

func (w *redirectToOK) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *redirectToOK) WriteHeader(code int) {
	if loc := w.Header().Get(HeaderLocation); loc != "" {
		w.Header().Del(HeaderLocation)
		w.Header().Set("Hx-Redirect", loc)
		code = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(code)
}

func TestCaptureSeesWhatAnInnerWriterMakesOfTheAnswer(t *testing.T) {
	var got Recorded
	r := newTestRouter()
	r.Use(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			stop := c.Response().Capture(64)
			err := next(c)
			got = stop()
			return err
		}
	}, func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			res := c.Response()
			w := &redirectToOK{ResponseWriter: res.ResponseWriter}
			res.ResponseWriter = w
			defer func() { res.ResponseWriter = w.ResponseWriter }()
			return next(c)
		}
	})
	r.POST("/", func(c *tctx) error { return c.Redirect(http.StatusSeeOther, "/next") })

	rec := do(r, http.MethodPost, "/")
	if rec.Code != http.StatusOK || got.Status != http.StatusOK {
		t.Fatalf("client got %d, recorded %d; want 200 for both", rec.Code, got.Status)
	}
	if got.Header.Get("Hx-Redirect") != "/next" || got.Header.Get(HeaderLocation) != "" {
		t.Errorf("recorded header = %v, want HX-Redirect and no Location", got.Header)
	}
}

func TestCaptureStopRestoresTheWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	res := &Response{ResponseWriter: rec}
	stop := res.Capture(8)
	if res.ResponseWriter == http.ResponseWriter(rec) {
		t.Fatal("Capture left the writer in place")
	}
	if _, err := res.WriteString("hi"); err != nil {
		t.Fatal(err)
	}
	first := stop()
	if res.ResponseWriter != http.ResponseWriter(rec) {
		t.Fatalf("writer after stop = %T, want the recorder", res.ResponseWriter)
	}
	second := stop()
	if second.Status != first.Status || string(second.Body) != string(first.Body) || second.Truncated != first.Truncated {
		t.Errorf("a second stop reported %+v, want %+v", second, first)
	}
}

func TestCaptureStoppedOutOfOrderPassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	res := &Response{ResponseWriter: rec}
	stopOuter := res.Capture(8)
	outer := res.ResponseWriter
	stopInner := res.Capture(8)

	stopOuter()
	if _, err := res.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	if got := stopInner(); string(got.Body) != "x" {
		t.Errorf("inner recorded %q, want %q", got.Body, "x")
	}
	if res.ResponseWriter != outer {
		t.Fatalf("writer after the inner stop = %T, want the outer capture", res.ResponseWriter)
	}
	if _, err := res.WriteString("y"); err != nil {
		t.Fatal(err)
	}
	if got := stopOuter(); len(got.Body) != 0 {
		t.Errorf("outer recorded %q after its stop, want nothing", got.Body)
	}
	if rec.Body.String() != "xy" {
		t.Errorf("client got %q, want %q", rec.Body, "xy")
	}
}

func TestCaptureIgnoresAnInformationalStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	res := &Response{ResponseWriter: rec}
	stop := res.Capture(8)
	res.Header().Set("Link", "</style.css>; rel=preload")
	res.WriteHeader(http.StatusEarlyHints)
	if got := stop(); got.Status != 0 || got.Header != nil {
		t.Errorf("recorded %d %v for a 103, want nothing", got.Status, got.Header)
	}

	stop = res.Capture(8)
	res.WriteHeader(http.StatusEarlyHints)
	res.WriteHeader(http.StatusCreated)
	if got := stop(); got.Status != http.StatusCreated {
		t.Errorf("recorded %d, want 201", got.Status)
	}
}

func TestCaptureAfterTheHeaderWentOut(t *testing.T) {
	rec := httptest.NewRecorder()
	res := &Response{ResponseWriter: rec}
	res.Header().Set("X-Early", "1")
	res.WriteHeader(http.StatusAccepted)

	stop := res.Capture(8)
	if _, err := res.WriteString("late"); err != nil {
		t.Fatal(err)
	}
	got := stop()
	if got.Status != http.StatusAccepted || got.Header.Get("X-Early") != "1" || string(got.Body) != "late" {
		t.Errorf("recorded %d %v %q, want 202 with X-Early and %q", got.Status, got.Header, got.Body, "late")
	}

	upgraded := &Response{ResponseWriter: httptest.NewRecorder(), Status: http.StatusSwitchingProtocols, Committed: true}
	if got := upgraded.Capture(8)(); got.Status != 0 {
		t.Errorf("recorded %d after a 101, want 0", got.Status)
	}
}

type failingWriter struct {
	*httptest.ResponseRecorder
}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("the client went away") }

func (failingWriter) WriteString(string) (int, error) { return 0, errors.New("the client went away") }

func TestCaptureRecordsWhatTheHandlerMeantWhenTheWriteFails(t *testing.T) {
	res := &Response{ResponseWriter: failingWriter{httptest.NewRecorder()}}
	stop := res.Capture(16)
	if _, err := res.WriteString("lost"); err == nil {
		t.Fatal("the write did not fail")
	}
	if _, err := res.Write([]byte("!")); err == nil {
		t.Fatal("the write did not fail")
	}
	if got := stop(); got.Status != http.StatusOK || string(got.Body) != "lost!" {
		t.Errorf("recorded %d %q, want 200 %q", got.Status, got.Body, "lost!")
	}
}

func TestCaptureRejectsANegativeLimit(t *testing.T) {
	defer func() {
		if msg, _ := recover().(string); !strings.Contains(msg, "Response.Capture needs a limit") {
			t.Errorf("panic = %q, want one about the limit", msg)
		}
	}()
	new(Response).Capture(-1)
}
