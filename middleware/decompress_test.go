package middleware_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// decompressPayload is what the binding route of the decompress tests decodes.
type decompressPayload struct {
	Name string `json:"name"`
}

// gzipped returns s as a gzip stream.
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

// decompressRouter binds on one route and reads the body itself on the other.
func decompressRouter(cfg middleware.DecompressConfig) *router.Router[*appContext] {
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
		return c.Stringf(http.StatusOK, "%d", len(body))
	})
	r.POST("/headers", func(c *appContext) error {
		req := c.Request()
		return c.Stringf(http.StatusOK, "%q %d",
			req.Header.Get(router.HeaderContentEncoding), req.ContentLength)
	})
	return r
}

// decompressPost builds a POST that names gzip and carries body.
func decompressPost(target string, body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	req.Header.Set(router.HeaderContentType, router.MIMEApplicationJSON)
	req.Header.Set(router.HeaderContentEncoding, "gzip")
	return req
}

func TestDecompressExpandsAGzipBody(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig{})

	rec := do(r, decompressPost("/bind", gzipped(t, `{"name":"ada"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec.Body.String() != "ada" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "ada")
	}
}

func TestDecompressClearsTheHeadersOfTheCompressedBody(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig{})

	rec := do(r, decompressPost("/headers", gzipped(t, "0123456789")))
	if got, want := rec.Body.String(), `"" -1`; got != want {
		t.Errorf("the handler saw %s, want %s", got, want)
	}
}

// TestDecompressLeavesTheRequestThatCameInAlone pins that the two header
// deletions land on a copy.
//
// The middleware hands the handler a shallow copy of the request, which shares
// the header map with the one that came in, and that one belongs to the
// server. A middleware above this one reads the request as the client sent it.
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
		return c.Stringf(http.StatusOK, "%d", len(body))
	})

	if rec := do(r, decompressPost("/read", gzipped(t, "0123456789"))); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if encoding != "gzip" {
		t.Errorf("the request that came in reads Content-Encoding %q, want %q", encoding, "gzip")
	}
}

func TestDecompressLeavesAPlainBodyAlone(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig{})

	req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader(`{"name":"ada"}`))
	req.Header.Set(router.HeaderContentType, router.MIMEApplicationJSON)
	if rec := do(r, req); rec.Body.String() != "ada" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "ada")
	}
}

func TestDecompressPassesAnEncodingItDoesNotKnow(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig{})

	req := httptest.NewRequest(http.MethodPost, "/read", strings.NewReader("0123456789"))
	req.Header.Set(router.HeaderContentEncoding, "br")
	rec := do(r, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec.Body.String() != "10" {
		t.Errorf("the handler read %q bytes, want the body untouched", rec.Body.String())
	}
}

func TestDecompressRejectsABodyThatIsNotGzip(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig{})

	rec := do(r, decompressPost("/read", []byte("this is not a gzip stream")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestDecompressPassesAnEmptyBodyThrough(t *testing.T) {
	r := decompressRouter(middleware.DecompressConfig{})

	rec := do(r, decompressPost("/read", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec.Body.String() != "0" {
		t.Errorf("the handler read %q bytes, want 0", rec.Body.String())
	}
}

func TestDecompressStopsAZipBomb(t *testing.T) {
	// Forty kilobytes of zeros compress to a few dozen bytes, which is what
	// makes a cap on the body that arrives no cap at all.
	bomb := gzipped(t, strings.Repeat("0", 40<<10))

	tests := []struct {
		name  string
		limit int64
		want  int
	}{
		{name: "a cap below the body", limit: 1024, want: http.StatusRequestEntityTooLarge},
		{name: "a cap above the body", limit: 1 << 20, want: http.StatusOK},
		{name: "no cap at all", limit: -1, want: http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := decompressRouter(middleware.DecompressConfig{MaxDecompressedSize: tc.limit})
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
	r := decompressRouter(middleware.DecompressConfig{Skip: skipPath("/read")})

	body := gzipped(t, "0123456789")
	rec := do(r, decompressPost("/read", body))
	if rec.Body.String() != strconv.Itoa(len(body)) {
		t.Errorf("the handler read %q bytes, want the %d compressed ones",
			rec.Body.String(), len(body))
	}
}
