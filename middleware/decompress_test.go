package middleware_test

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

type decompressPayload struct {
	Name string `json:"name"`
}

func gzipped(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatalf("write the gzip body: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close the gzip body: %v", err)
	}
	return buf.Bytes()
}

func deflated(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatalf("write the deflate body: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close the deflate body: %v", err)
	}
	return buf.Bytes()
}

func decompressRouter(cfg middleware.DecompressConfig[*appContext]) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.DecompressWithConfig[*appContext](cfg))
	r.POST("/bind", func(c *appContext) error {
		in, err := c.Bind[decompressPayload]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, in.Name)
	})
	r.POST("/read", func(c *appContext) error {
		body, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%d", len(body)))
	})
	r.POST("/headers", func(c *appContext) error {
		req := c.Request()
		return c.String(http.StatusOK, fmt.Sprintf("%q %d",
			req.Header.Get(router.HeaderContentEncoding), req.ContentLength))
	})
	return r
}

func decompressPost(target string, body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	req.Header.Set(router.HeaderContentType, router.MIMEApplicationJSON)
	req.Header.Set(router.HeaderContentEncoding, "gzip")
	return req
}

func TestDecompressExpandsAGzipBody(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig[*appContext]{})

	rec := do(r, decompressPost("/bind", gzipped(t, `{"name":"ada"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec.Body.String() != "ada" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "ada")
	}
}

func TestDecompressClearsTheHeadersOfTheCompressedBody(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig[*appContext]{})

	rec := do(r, decompressPost("/headers", gzipped(t, "0123456789")))
	if got, want := rec.Body.String(), `"" -1`; got != want {
		t.Errorf("the handler saw %s, want %s", got, want)
	}
}

func TestDecompressLeavesTheRequestThatCameInAlone(t *testing.T) {
	var encoding string
	watch := func(next router.HandlerFunc[*appContext]) router.HandlerFunc[*appContext] {
		return func(c *appContext) error {
			came := c.Request()
			err := next(c)
			encoding = came.Header.Get(router.HeaderContentEncoding)
			return err
		}
	}

	r := newRouter()
	r.Use(watch, middleware.Decompress[*appContext])
	r.POST("/read", func(c *appContext) error {
		body, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%d", len(body)))
	})

	if rec := do(r, decompressPost("/read", gzipped(t, "0123456789"))); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if encoding != "gzip" {
		t.Errorf("the request that came in reads Content-Encoding %q, want %q", encoding, "gzip")
	}
}

func TestDecompressLeavesAPlainBodyAlone(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig[*appContext]{})

	req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader(`{"name":"ada"}`))
	req.Header.Set(router.HeaderContentType, router.MIMEApplicationJSON)
	if rec := do(r, req); rec.Body.String() != "ada" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "ada")
	}
}

func TestDecompressExpandsADeflateBody(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig[*appContext]{})

	req := decompressPost("/bind", deflated(t, `{"name":"ada"}`))
	req.Header.Set(router.HeaderContentEncoding, "Deflate")
	rec := do(r, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec.Body.String() != "ada" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "ada")
	}
}

func TestDecompressRefusesAnEncodingItDoesNotKnow(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig[*appContext]{})

	for _, encoding := range []string{"br", "zstd", "gzip, gzip", "deflate, gzip", "compress"} {
		t.Run(encoding, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/read", strings.NewReader("0123456789"))
			req.Header.Set(router.HeaderContentEncoding, encoding)
			rec := do(r, req)
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("status = %d, want 415: %s", rec.Code, rec.Body)
			}
			if got := rec.Header().Get(router.HeaderAcceptEncoding); got != "gzip, deflate" {
				t.Errorf("Accept-Encoding = %q, want %q", got, "gzip, deflate")
			}
		})
	}
}

func TestDecompressPassesIdentityThrough(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig[*appContext]{})

	for _, encoding := range []string{"identity", "Identity", ""} {
		t.Run(encoding, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/read", strings.NewReader("0123456789"))
			req.Header.Set(router.HeaderContentEncoding, encoding)
			if rec := do(r, req); rec.Code != http.StatusOK || rec.Body.String() != "10" {
				t.Errorf("status %d, the handler read %q bytes, want 200 and the body untouched",
					rec.Code, rec.Body.String())
			}
		})
	}
}

func TestDecompressExpandsGzipAfterIdentity(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig[*appContext]{})

	req := decompressPost("/bind", gzipped(t, `{"name":"ada"}`))
	req.Header.Set(router.HeaderContentEncoding, "identity, gzip")
	if rec := do(r, req); rec.Body.String() != "ada" {
		t.Errorf("status %d, body = %q, want %q", rec.Code, rec.Body.String(), "ada")
	}
}

func TestDecompressRejectsABodyThatIsNotDeflate(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig[*appContext]{})

	req := decompressPost("/read", []byte("this is not a zlib stream"))
	req.Header.Set(router.HeaderContentEncoding, "deflate")
	if rec := do(r, req); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestDecompressStopsADeflateBomb(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig[*appContext]{MaxBytes: 1 << 10})

	req := decompressPost("/read", deflated(t, strings.Repeat("0", 40<<10)))
	req.Header.Set(router.HeaderContentEncoding, "deflate")
	if rec := do(r, req); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestDecompressReusesADeflateReader(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig[*appContext]{})

	for i := range 3 {
		want := strings.Repeat("x", i+1)
		req := decompressPost("/read", deflated(t, want))
		req.Header.Set(router.HeaderContentEncoding, "deflate")
		if rec := do(r, req); rec.Body.String() != strconv.Itoa(len(want)) {
			t.Errorf("request %d: the handler read %q bytes, want %d", i, rec.Body.String(), len(want))
		}
	}
}

func TestDecompressRejectsABodyThatIsNotGzip(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig[*appContext]{})

	rec := do(r, decompressPost("/read", []byte("this is not a gzip stream")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestDecompressPassesAnEmptyBodyThrough(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig[*appContext]{})

	rec := do(r, decompressPost("/read", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec.Body.String() != "0" {
		t.Errorf("the handler read %q bytes, want 0", rec.Body.String())
	}
}

func TestDecompressStopsAZipBomb(t *testing.T) {
	bomb := gzipped(t, strings.Repeat("0", 40<<10))

	tests := []struct {
		name  string
		limit int64
		want  int
	}{
		{name: "a cap below the body", limit: 1024, want: http.StatusRequestEntityTooLarge},
		{name: "a cap above the body", limit: 1 << 20, want: http.StatusOK},
		{name: "the default cap", want: http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := decompressRouter(middleware.DecompressConfig[*appContext]{MaxBytes: tc.limit})
			if rec := do(r, decompressPost("/read", bomb)); rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestDecompressPlainFormExpandsTheBody(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Decompress[*appContext])
	r.POST("/", func(c *appContext) error {
		body, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, string(body))
	})

	rec := do(r, decompressPost("/", gzipped(t, "ada")))
	if rec.Body.String() != "ada" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "ada")
	}
}

func TestDecompressSkip(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig[*appContext]{Skip: skipPath("/read")})

	body := gzipped(t, "0123456789")
	rec := do(r, decompressPost("/read", body))
	if rec.Body.String() != strconv.Itoa(len(body)) {
		t.Errorf("the handler read %q bytes, want the %d compressed ones",
			rec.Body.String(), len(body))
	}
}

func decompressFailingRouter(read func(body []byte, err error)) *router.Router[*appContext] {
	r := newRouter()
	r.ErrorHandler(func(c *appContext, _ error) error {
		body, err := io.ReadAll(c.Request().Body)
		read(body, err)
		return nil
	})
	r.Use(middleware.DecompressWithConfig(middleware.DecompressConfig[*appContext]{}))
	r.POST("/fail", func(*appContext) error {
		return router.ErrBadRequest.WithMessage("validation failed")
	})
	return r
}

func TestDecompressBodyStopsAnsweringWhenTheChainReturns(t *testing.T) {
	var body []byte
	var err error
	r := decompressFailingRouter(func(b []byte, e error) { body, err = b, e })

	do(r, decompressPost("/fail", gzipped(t, `{"name":"ada"}`)))

	if err == nil {
		t.Fatalf("the read after the chain answered %q from a reader that went back to the pool", body)
	}
	if len(body) != 0 {
		t.Errorf("the read after the chain answered %q, want no bytes", body)
	}
}

func TestDecompressDoesNotHandOneReaderToTwoRequests(t *testing.T) {
	payload := gzipped(t, strings.Repeat("the quick brown fox\n", 2048))

	var leaked atomic.Int64
	r := decompressFailingRouter(func(body []byte, err error) {
		if err == nil || len(body) != 0 {
			leaked.Add(1)
		}
	})

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 32 {
				do(r, decompressPost("/fail", payload))
			}
		})
	}
	wg.Wait()

	if n := leaked.Load(); n != 0 {
		t.Errorf("%d reads after the chain reached a reader that another request holds", n)
	}
}

func TestDecompressRejectsANegativeCap(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("no panic for a negative MaxBytes")
		}
	}()
	middleware.DecompressWithConfig(middleware.DecompressConfig[*appContext]{MaxBytes: -1})
}

func TestDecompressTooLargeClosesTheConnection(t *testing.T) {
	r := newRouter()
	r.Use(middleware.DecompressWithConfig(middleware.DecompressConfig[*appContext]{MaxBytes: 10}))
	r.POST("/", func(c *appContext) error {
		_, err := io.ReadAll(c.Request().Body)
		return err
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL,
		bytes.NewReader(gzipped(t, strings.Repeat("a", 1000))))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(router.HeaderContentEncoding, "gzip")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", res.StatusCode)
	}
	if !res.Close {
		t.Error("the connection stays open after a 413")
	}
}
