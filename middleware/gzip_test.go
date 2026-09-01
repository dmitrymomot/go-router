package middleware_test

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
	"github.com/dmitrymomot/go-router/routertest"
)

var gzipLongBody = strings.Repeat("<p>the quick brown fox</p>", 100)

func gzipRouter(cfg middleware.GzipConfig) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.GzipWithConfig[*appContext](cfg))
	r.GET("/long", func(c *appContext) error {
		return c.HTML(http.StatusOK, gzipLongBody)
	})
	r.GET("/short", func(c *appContext) error {
		return c.String(http.StatusOK, "ok")
	})
	r.GET("/empty", func(c *appContext) error {
		return c.NoContent(http.StatusNoContent)
	})
	r.GET("/unchanged", func(c *appContext) error {
		return c.NoContent(http.StatusNotModified)
	})
	r.GET("/encoded", func(c *appContext) error {
		c.SetHeader(router.HeaderContentEncoding, "br")
		return c.HTML(http.StatusOK, gzipLongBody)
	})
	r.GET("/sniff", func(c *appContext) error {
		_, err := c.Response().Write([]byte(gzipLongBody))
		return err
	})
	r.GET("/events", func(c *appContext) error {
		s, err := c.SSE(http.StatusOK)
		if err != nil {
			return err
		}
		return s.Send(router.Event{ID: "1", Name: "tick", Data: "one"})
	})
	return r
}

func gzipGet(h http.Handler, target, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if accept != "" {
		req.Header.Set(router.HeaderAcceptEncoding, accept)
	}
	return do(h, req)
}

func ungzip(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("read the gzip header: %v", err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read the gzip body: %v", err)
	}
	return string(body)
}

func TestGzipCompressesALongBody(t *testing.T) {
	r := gzipRouter(middleware.GzipConfig{})

	rec := gzipGet(r, "/long", "gzip")
	if got := rec.Header().Get(router.HeaderContentEncoding); got != "gzip" {
		t.Fatalf("content encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get(router.HeaderContentLength); got != "" {
		t.Errorf("content length = %q, want none: it describes the body that the handler wrote", got)
	}
	if body := ungzip(t, rec); body != gzipLongBody {
		t.Errorf("the body reads back as %d bytes, want %d", len(body), len(gzipLongBody))
	}
	if rec.Body.Len() >= len(gzipLongBody) {
		t.Errorf("the compressed body is %d bytes, which saves nothing on %d",
			rec.Body.Len(), len(gzipLongBody))
	}
}

func TestGzipLeavesAShortBodyAlone(t *testing.T) {
	r := gzipRouter(middleware.GzipConfig{})

	rec := gzipGet(r, "/short", "gzip")
	if got := rec.Header().Get(router.HeaderContentEncoding); got != "" {
		t.Errorf("content encoding = %q, want none", got)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "ok")
	}
}

func TestGzipMinLengthDecidesWhatIsWorthCompressing(t *testing.T) {
	r := gzipRouter(middleware.GzipConfig{MinLength: 1})

	rec := gzipGet(r, "/short", "gzip")
	if got := rec.Header().Get(router.HeaderContentEncoding); got != "gzip" {
		t.Fatalf("content encoding = %q, want gzip", got)
	}
	if body := ungzip(t, rec); body != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
}

func TestGzipAlwaysSendsVary(t *testing.T) {
	r := gzipRouter(middleware.GzipConfig{})

	for _, accept := range []string{"gzip", "", "identity"} {
		rec := gzipGet(r, "/long", accept)
		if got := rec.Header().Get(router.HeaderVary); got != router.HeaderAcceptEncoding {
			t.Errorf("vary = %q for Accept-Encoding %q, want %q",
				got, accept, router.HeaderAcceptEncoding)
		}
	}
}

func TestGzipReadsTheAcceptEncodingHeader(t *testing.T) {
	tests := []struct {
		name   string
		accept string
		want   bool
	}{
		{name: "the plain token", accept: "gzip", want: true},
		{name: "no header at all", accept: "", want: false},
		{name: "another encoding", accept: "br", want: false},
		{name: "identity alone", accept: "identity", want: false},
		{name: "a refusal", accept: "gzip;q=0", want: false},
		{name: "a quality of its own", accept: "gzip;q=0.5", want: true},
		{name: "one of several", accept: "br, gzip, deflate", want: true},
		{name: "the wildcard", accept: "*", want: true},
		{name: "a refused wildcard", accept: "*;q=0", want: false},
		{name: "a refusal beside a wildcard", accept: "*, gzip;q=0", want: false},
		{name: "an upper case token", accept: "GZIP", want: true},
	}

	r := gzipRouter(middleware.GzipConfig{})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := gzipGet(r, "/long", tc.accept)
			got := rec.Header().Get(router.HeaderContentEncoding) == "gzip"
			if got != tc.want {
				t.Errorf("compressed = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestGzipPassesAnEventStreamThrough(t *testing.T) {
	r := gzipRouter(middleware.GzipConfig{MinLength: 1})

	rec := gzipGet(r, "/events", "gzip")
	if got := rec.Header().Get(router.HeaderContentEncoding); got != "" {
		t.Fatalf("content encoding = %q, want none: a buffered stream reaches nobody", got)
	}
	if !strings.Contains(rec.Body.String(), "data: one") {
		t.Errorf("body = %q, want the event as it was written", rec.Body.String())
	}
}

func TestGzipDeliversEachEventAsItIsSent(t *testing.T) {
	sent := []string{"one", "two", "three"}

	w := &flushWatcher{ResponseRecorder: httptest.NewRecorder()}
	r := newRouter()
	r.Use(middleware.GzipWithConfig[*appContext](middleware.GzipConfig{MinLength: 1}))
	r.GET("/events", func(c *appContext) error {
		s, err := c.SSE(http.StatusOK)
		if err != nil {
			return err
		}
		for _, data := range sent {
			if err := s.Send(router.Event{Name: "tick", Data: data}); err != nil {
				return err
			}
			if got := w.Body.String(); !strings.Contains(got, "data: "+data) {
				t.Errorf("event %q has not reached the client while the stream is open; it holds %q", data, got)
			}
		}
		return nil
	})

	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	req.Header.Set(router.HeaderAcceptEncoding, "gzip")
	r.ServeHTTP(w, req)

	if got := w.Header().Get(router.HeaderContentEncoding); got != "" {
		t.Errorf("content encoding = %q, want none", got)
	}
	if got := w.Header().Get(router.HeaderContentType); got != router.MIMETextEventStream {
		t.Errorf("content type = %q, want %q", got, router.MIMETextEventStream)
	}
	if len(w.flushed) < len(sent) {
		t.Errorf("the client saw %d flushes, want one per event at least", len(w.flushed))
	}
}

type flushWatcher struct {
	*httptest.ResponseRecorder
	flushed []int
}

func (w *flushWatcher) Flush() { w.flushed = append(w.flushed, w.Body.Len()) }

func TestGzipFlushReachesTheClientBeforeTheBodyEnds(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Gzip[*appContext])
	r.GET("/stream", func(c *appContext) error {
		res := c.Response()
		if _, err := res.Write([]byte("<html>")); err != nil {
			return err
		}
		res.Flush()
		_, err := res.Write([]byte(gzipLongBody))
		return err
	})

	w := &flushWatcher{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	req.Header.Set(router.HeaderAcceptEncoding, "gzip")
	r.ServeHTTP(w, req)

	if len(w.flushed) == 0 {
		t.Fatal("the wrapper swallowed the flush of the handler")
	}
	if w.flushed[0] == 0 {
		t.Error("the flush reached the client with an empty body, so the bytes sat in the buffer")
	}
	if got := w.Header().Get(router.HeaderContentEncoding); got != "gzip" {
		t.Fatalf("content encoding = %q, want gzip", got)
	}
	if body := ungzip(t, w.ResponseRecorder); body != "<html>"+gzipLongBody {
		t.Errorf("the body reads back as %d bytes, want %d", len(body), len("<html>")+len(gzipLongBody))
	}
}

func TestGzipFlushBeforeTheFirstWriteReachesTheClient(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Gzip[*appContext])
	r.GET("/early", func(c *appContext) error {
		res := c.Response()
		res.WriteHeader(http.StatusOK)
		res.Flush()
		_, err := res.Write([]byte(gzipLongBody))
		return err
	})

	w := &flushWatcher{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/early", nil)
	req.Header.Set(router.HeaderAcceptEncoding, "gzip")
	r.ServeHTTP(w, req)

	if len(w.flushed) == 0 {
		t.Fatal("the wrapper swallowed a flush that carried no body of its own")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 at the flush", w.Code)
	}
	if body := ungzip(t, w.ResponseRecorder); body != gzipLongBody {
		t.Errorf("the body reads back as %d bytes, want %d", len(body), len(gzipLongBody))
	}
}

func TestGzipHeadUsesTheGetRepresentationHeadersWithoutABody(t *testing.T) {
	r := gzipRouter(middleware.GzipConfig{})
	getRec := gzipGet(r, "/long", "gzip")

	req := httptest.NewRequest(http.MethodHead, "/long", nil)
	req.Header.Set(router.HeaderAcceptEncoding, "gzip")
	rec := do(r, req)

	if got, want := rec.Header().Get(router.HeaderContentEncoding),
		getRec.Header().Get(router.HeaderContentEncoding); got != want {
		t.Errorf("content encoding = %q, want GET's %q", got, want)
	}
	if got, want := rec.Header().Get(router.HeaderContentLength),
		getRec.Header().Get(router.HeaderContentLength); got != want {
		t.Errorf("content length = %q, want GET's %q", got, want)
	}
	if got, want := rec.Header().Get(router.HeaderContentType),
		getRec.Header().Get(router.HeaderContentType); got != want {
		t.Errorf("content type = %q, want GET's %q", got, want)
	}
	if got := rec.Header().Get(router.HeaderVary); got != router.HeaderAcceptEncoding {
		t.Errorf("vary = %q, want %q", got, router.HeaderAcceptEncoding)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body is %d bytes, want none", rec.Body.Len())
	}
}

func TestGzipHeadKeepsAShortRepresentationPlain(t *testing.T) {
	r := gzipRouter(middleware.GzipConfig{})
	getRec := gzipGet(r, "/short", "gzip")

	req := httptest.NewRequest(http.MethodHead, "/short", nil)
	req.Header.Set(router.HeaderAcceptEncoding, "gzip")
	rec := do(r, req)

	for _, name := range []string{
		router.HeaderContentEncoding,
		router.HeaderContentLength,
		router.HeaderContentType,
	} {
		if got, want := rec.Header().Get(name), getRec.Header().Get(name); got != want {
			t.Errorf("%s = %q, want GET's %q", name, got, want)
		}
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body is %d bytes, want none", rec.Body.Len())
	}
}

func TestGzipLeavesAStatusWithoutABodyAlone(t *testing.T) {
	r := gzipRouter(middleware.GzipConfig{MinLength: 1})

	for path, want := range map[string]int{"/empty": 204, "/unchanged": 304} {
		rec := gzipGet(r, path, "gzip")
		if rec.Code != want {
			t.Fatalf("status = %d for %s, want %d", rec.Code, path, want)
		}
		if got := rec.Header().Get(router.HeaderContentEncoding); got != "" {
			t.Errorf("content encoding = %q for %s, want none", got, path)
		}
	}
}

func TestGzipDoesNotEncodeTwice(t *testing.T) {
	r := gzipRouter(middleware.GzipConfig{})

	rec := gzipGet(r, "/encoded", "gzip")
	if got := rec.Header().Get(router.HeaderContentEncoding); got != "br" {
		t.Fatalf("content encoding = %q, want the one the handler set", got)
	}
	if rec.Body.String() != gzipLongBody {
		t.Error("the body of a handler that encodes its own body was encoded again")
	}
}

func TestGzipSetsTheContentTypeFromTheUncompressedBody(t *testing.T) {
	r := gzipRouter(middleware.GzipConfig{})

	rec := gzipGet(r, "/sniff", "gzip")
	if got := rec.Header().Get(router.HeaderContentType); !strings.HasPrefix(got, "text/") {
		t.Errorf("content type = %q, want the type of the body that the handler wrote", got)
	}
	if body := ungzip(t, rec); body != gzipLongBody {
		t.Errorf("the body reads back as %d bytes, want %d", len(body), len(gzipLongBody))
	}
}

func TestGzipCountsTheCompressedSize(t *testing.T) {
	var size int64
	r := newRouter()
	r.Use(func(next router.HandlerFunc[*appContext]) router.HandlerFunc[*appContext] {
		return func(c *appContext) error {
			err := next(c)
			size = c.Response().Size
			return err
		}
	})
	r.Use(middleware.Gzip[*appContext])
	r.GET("/long", func(c *appContext) error { return c.HTML(http.StatusOK, gzipLongBody) })

	rec := gzipGet(r, "/long", "gzip")
	if size != int64(rec.Body.Len()) {
		t.Errorf("the response counted %d bytes, want the %d that reached the client",
			size, rec.Body.Len())
	}
}

func TestGzipLeavesAnUnfinishedStreamAfterAPanic(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Recover[*appContext])
	r.Use(middleware.Gzip[*appContext])
	r.GET("/boom", func(c *appContext) error {
		if _, err := c.Response().Write([]byte(gzipLongBody)); err != nil {
			return err
		}
		panic("half a page")
	})

	rec := gzipGet(r, "/boom", "gzip")
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		return
	}
	if _, err := io.ReadAll(zr); err == nil {
		t.Error("the stream ends cleanly, so the client reads half a page as the whole one")
	}
}

func TestGzipClampsTheLevel(t *testing.T) {
	for _, level := range []int{0, 1, 9, 42, -1, -2, -99} {
		r := gzipRouter(middleware.GzipConfig{Level: level})
		rec := gzipGet(r, "/long", "gzip")
		if got := rec.Header().Get(router.HeaderContentEncoding); got != "gzip" {
			t.Fatalf("content encoding = %q at level %d, want gzip", got, level)
		}
		if body := ungzip(t, rec); body != gzipLongBody {
			t.Errorf("the body at level %d reads back as %d bytes, want %d",
				level, len(body), len(gzipLongBody))
		}
	}
}

func TestGzipPlainFormCompresses(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Gzip[*appContext])
	r.GET("/long", func(c *appContext) error { return c.HTML(http.StatusOK, gzipLongBody) })

	rec := gzipGet(r, "/long", "gzip")
	if got := rec.Header().Get(router.HeaderContentEncoding); got != "gzip" {
		t.Fatalf("content encoding = %q, want gzip", got)
	}
	if body := ungzip(t, rec); body != gzipLongBody {
		t.Errorf("the body reads back as %d bytes, want %d", len(body), len(gzipLongBody))
	}
}

func TestGzipServesARealConnection(t *testing.T) {
	srv := httptest.NewServer(gzipRouter(middleware.GzipConfig{}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/long")
	if err != nil {
		t.Fatalf("get the page: %v", err)
	}
	defer res.Body.Close() //nolint:errcheck // The test is done with it.

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read the page: %v", err)
	}
	if !res.Uncompressed {
		t.Error("the transport read a plain body, so nothing was compressed")
	}
	if string(body) != gzipLongBody {
		t.Errorf("the page reads back as %d bytes, want %d", len(body), len(gzipLongBody))
	}
}

func TestGzipKeepsTheStatusOfTheHandler(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Gzip[*appContext])
	r.GET("/made", func(c *appContext) error { return c.HTML(http.StatusCreated, gzipLongBody) })

	rec := gzipGet(r, "/made", "gzip")
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Code)
	}
	if got := rec.Header().Get(router.HeaderContentEncoding); got != "gzip" {
		t.Errorf("content encoding = %q, want gzip", got)
	}
}

func TestGzipSkip(t *testing.T) {
	r := gzipRouter(middleware.GzipConfig{Skip: skipPath("/long")})

	rec := gzipGet(r, "/long", "gzip")
	if got := rec.Header().Get(router.HeaderContentEncoding); got != "" {
		t.Errorf("content encoding = %q, want none", got)
	}
	if got := rec.Header().Get(router.HeaderVary); got != "" {
		t.Errorf("vary = %q, want none: a skipped request never reached the middleware", got)
	}
}

func gzipFlushRouter(t *testing.T, cfg middleware.GzipConfig, h router.HandlerFunc[*appContext]) *flushWatcher {
	t.Helper()
	r := newRouter()
	r.Use(middleware.GzipWithConfig[*appContext](cfg))
	r.GET("/early", h)

	w := &flushWatcher{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/early", nil)
	req.Header.Set(router.HeaderAcceptEncoding, "gzip")
	r.ServeHTTP(w, req)
	return w
}

func TestGzipFlushWithoutAStatusCompressesTheBodyItAnnounces(t *testing.T) {
	w := gzipFlushRouter(t, middleware.GzipConfig{}, func(c *appContext) error {
		res := c.Response()
		res.Flush()
		_, err := res.Write([]byte(gzipLongBody))
		return err
	})

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get(router.HeaderContentEncoding); got != "gzip" {
		t.Fatalf("content encoding = %q, want gzip", got)
	}
	if body := ungzip(t, w.ResponseRecorder); body != gzipLongBody {
		t.Errorf("the body reads back as %d bytes, want %d", len(body), len(gzipLongBody))
	}
}

func TestGzipFlushWithoutAStatusCommitsTheResponse(t *testing.T) {
	w := gzipFlushRouter(t, middleware.GzipConfig{}, func(c *appContext) error {
		c.Response().Flush()
		return errors.New("the render failed halfway")
	})

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: the flush answered the header already", w.Code)
	}
	if body := ungzip(t, w.ResponseRecorder); body != "" {
		t.Errorf("body = %q, want none", body)
	}
}

func TestGzipFlushWithoutAStatusLeavesAnEventStreamAlone(t *testing.T) {
	w := gzipFlushRouter(t, middleware.GzipConfig{MinLength: 1}, func(c *appContext) error {
		res := c.Response()
		res.Header().Set(router.HeaderContentType, router.MIMETextEventStream)
		res.Flush()
		_, err := res.WriteString("data: one\n\n")
		return err
	})

	if got := w.Header().Get(router.HeaderContentEncoding); got != "" {
		t.Errorf("content encoding = %q, want none: an event stream in a compression buffer arrives at the end", got)
	}
	if got := w.Body.String(); got != "data: one\n\n" {
		t.Errorf("body = %q, want the event as it was written", got)
	}
}

type statusRecorder struct {
	http.ResponseWriter
	codes []int
}

func (s *statusRecorder) WriteHeader(code int) { s.codes = append(s.codes, code) }

func (s *statusRecorder) Write(p []byte) (int, error) { return len(p), nil }

func (s *statusRecorder) Flush() {}

func TestGzipCommitsASwitchingProtocols(t *testing.T) {
	tests := []struct {
		name    string
		handler func(*appContext) error
	}{
		{"the handler writes the status alone", func(c *appContext) error {
			c.Response().WriteHeader(http.StatusSwitchingProtocols)
			return nil
		}},
		{"the handler flushes after it", func(c *appContext) error {
			c.Response().WriteHeader(http.StatusSwitchingProtocols)
			c.Response().Flush()
			return nil
		}},
		{"the handler writes to the upgraded connection", func(c *appContext) error {
			c.Response().WriteHeader(http.StatusSwitchingProtocols)
			_, err := c.Response().Write([]byte(gzipLongBody))
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRouter()
			r.Use(middleware.GzipWithConfig[*appContext](middleware.GzipConfig{MinLength: 1}))
			r.GET("/upgrade", tt.handler)

			rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
			req := httptest.NewRequest(http.MethodGet, "/upgrade", nil)
			req.Header.Set(router.HeaderAcceptEncoding, "gzip")
			r.ServeHTTP(rec, req)

			if len(rec.codes) != 1 || rec.codes[0] != http.StatusSwitchingProtocols {
				t.Errorf("status lines = %v, want one 101", rec.codes)
			}
			if got := rec.Header().Get(router.HeaderContentEncoding); got != "" {
				t.Errorf("content encoding = %q, want none on an upgraded connection", got)
			}
		})
	}
}

// assertHEADMatchesGET is the rule stated in the root package's head_test.go: a
// HEAD reply carries the status and the headers its GET would carry, and no
// body. Each package keeps its own copy, because routertest cannot hold one.
func assertHEADMatchesGET(t *testing.T, h http.Handler, target string, opts ...routertest.RequestOption) {
	t.Helper()
	get := routertest.Do(h, http.MethodGet, target, opts...)
	head := routertest.Do(h, http.MethodHead, target, opts...)

	if head.StatusCode != get.StatusCode {
		t.Errorf("HEAD %s: status = %d, want the %d of the GET", target, head.StatusCode, get.StatusCode)
	}
	if len(head.Body) != 0 {
		t.Errorf("HEAD %s: body = %q, want none", target, head.Body)
	}
	if !maps.EqualFunc(head.Header, get.Header, slices.Equal) {
		t.Errorf("HEAD %s: headers = %v, want the %v of the GET", target, head.Header, get.Header)
	}
}

// The nil gzip.Writer crash lived here: HEAD stopped being short-circuited, so
// a handler that flushed reached a writer that had never been opened. The rule
// is assertHEADMatchesGET's — the reply carries the headers of the
// GET, compressed ones included, and no body.
func TestGzipAnswersHEADWithTheHeadersOfTheGET(t *testing.T) {
	tests := []struct {
		name     string
		handler  router.HandlerFunc[*appContext]
		encoding string
	}{
		{"the handler writes a body", func(c *appContext) error {
			return c.HTML(http.StatusOK, gzipLongBody)
		}, "gzip"},
		{"the handler flushes", func(c *appContext) error {
			if err := c.HTML(http.StatusOK, gzipLongBody); err != nil {
				return err
			}
			c.Response().Flush()
			return nil
		}, "gzip"},
		// A flush with nothing written yet: on HEAD the writer is never opened,
		// which is the shape that used to dereference a nil gzip.Writer.
		{"the handler flushes before writing anything", func(c *appContext) error {
			c.Response().WriteHeader(http.StatusOK)
			c.Response().Flush()
			return nil
		}, "gzip"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var failed error
			r := newRouter()
			// The status line is already out by the time a flush panics, so the
			// reply stays a 200 with no body and matches its GET. Only the
			// error handler sees the panic.
			r.ErrorHandler(func(_ *appContext, err error) { failed = err })
			r.Use(middleware.GzipWithConfig[*appContext](middleware.GzipConfig{MinLength: 1}))
			r.GET("/x", tt.handler)

			get := routertest.Get(r, "/x", routertest.Header(router.HeaderAcceptEncoding, "gzip"))
			if got := get.Header.Get(router.HeaderContentEncoding); got != tt.encoding {
				t.Fatalf("the GET sent Content-Encoding %q, want %q; this case no longer reaches the path it names", got, tt.encoding)
			}

			assertHEADMatchesGET(t, r, "/x", routertest.Header(router.HeaderAcceptEncoding, "gzip"))
			if failed != nil {
				t.Errorf("the request failed: %v", failed)
			}
		})
	}
}
