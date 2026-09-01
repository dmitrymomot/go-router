package router

import (
	"context"
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
