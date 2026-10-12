package middleware_test

import (
	"bytes"
	"compress/zlib"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// pngBytes is a PNG signature followed by filler, enough for sniffing.
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 4096)...)

// writerRouter answers GET / by running write against the response.
func writerRouter(cfg middleware.GzipConfig[*appContext], write func(c *appContext) error) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.GzipWithConfig[*appContext](cfg))
	r.GET("/", write)
	return r
}

func TestGzipLargeWrites(t *testing.T) {
	big := strings.Repeat("<p>the quick brown fox</p>", 4000)
	tests := []struct {
		name  string
		write func(c *appContext) error
	}{
		{"one write", func(c *appContext) error {
			_, err := c.Response().Write([]byte(big))
			return err
		}},
		{"small then large", func(c *appContext) error {
			if _, err := c.Response().Write([]byte(big[:100])); err != nil {
				return err
			}
			_, err := c.Response().Write([]byte(big[100:]))
			return err
		}},
		{"fills the buffer partway", func(c *appContext) error {
			if _, err := c.Response().Write([]byte(big[:1000])); err != nil {
				return err
			}
			n, err := c.Response().Write([]byte(big[1000:]))
			if want := len(big) - 1000; n != want {
				t.Errorf("Write reported %d bytes, want %d", n, want)
			}
			return err
		}},
		{"string", func(c *appContext) error {
			n, err := c.Response().WriteString(big)
			if n != len(big) {
				t.Errorf("WriteString reported %d bytes, want %d", n, len(big))
			}
			return err
		}},
		{"strings in pieces", func(c *appContext) error {
			for p := big; p != ""; {
				n := min(777, len(p))
				if _, err := c.Response().WriteString(p[:n]); err != nil {
					return err
				}
				p = p[n:]
			}
			return nil
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := writerRouter(middleware.GzipConfig[*appContext]{}, tc.write)
			rec := gzipGet(r, "/", "gzip")
			if got := rec.Header().Get(router.HeaderContentEncoding); got != "gzip" {
				t.Fatalf("Content-Encoding = %q, want gzip", got)
			}
			if got := rec.Header().Get(router.HeaderContentType); !strings.HasPrefix(got, "text/html") {
				t.Errorf("Content-Type = %q, want the sniffed text/html", got)
			}
			if got := ungzip(t, rec); got != big {
				t.Errorf("body differs after a round trip: %d bytes, want %d", len(got), len(big))
			}
		})
	}
}

func TestGzipWriteStringSmallBodyStaysPlain(t *testing.T) {
	r := writerRouter(middleware.GzipConfig[*appContext]{}, func(c *appContext) error {
		_, err := c.Response().WriteString("tiny")
		return err
	})
	rec := gzipGet(r, "/", "gzip")
	if rec.Header().Get(router.HeaderContentEncoding) != "" || rec.Body.String() != "tiny" {
		t.Errorf("got %q encoded %q, want a plain tiny", rec.Body.String(), rec.Header().Get(router.HeaderContentEncoding))
	}
}

func TestGzipSkipsCompressedTypes(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		cfg         middleware.GzipConfig[*appContext]
		compressed  bool
	}{
		{"png header", "image/png", middleware.GzipConfig[*appContext]{}, false},
		{"png sniffed", "", middleware.GzipConfig[*appContext]{}, false},
		{"jpeg", "image/jpeg", middleware.GzipConfig[*appContext]{}, false},
		{"svg", "image/svg+xml", middleware.GzipConfig[*appContext]{}, true},
		{"video", "video/mp4", middleware.GzipConfig[*appContext]{}, false},
		{"audio", "audio/mpeg", middleware.GzipConfig[*appContext]{}, false},
		{"woff2", "font/woff2", middleware.GzipConfig[*appContext]{}, false},
		{"ttf", "font/ttf", middleware.GzipConfig[*appContext]{}, true},
		{"pdf with parameters and case", "Application/PDF; x=y", middleware.GzipConfig[*appContext]{}, false},
		{"octet-stream", "application/octet-stream", middleware.GzipConfig[*appContext]{}, false},
		{"text", "text/plain", middleware.GzipConfig[*appContext]{}, true},
		{"json", "application/json", middleware.GzipConfig[*appContext]{}, true},
		{"custom replaces the defaults", "image/png", middleware.GzipConfig[*appContext]{ExcludeContentTypes: []string{"text/*"}}, true},
		{"custom wildcard", "text/plain", middleware.GzipConfig[*appContext]{ExcludeContentTypes: []string{"text/*"}}, false},
		{"custom exact", "application/json", middleware.GzipConfig[*appContext]{ExcludeContentTypes: []string{"Application/JSON"}}, false},
		{"empty list excludes nothing", "image/png", middleware.GzipConfig[*appContext]{ExcludeContentTypes: []string{}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := writerRouter(tc.cfg, func(c *appContext) error {
				if tc.contentType != "" {
					c.SetHeader(router.HeaderContentType, tc.contentType)
				}
				body := pngBytes
				if tc.contentType != "" && !strings.HasPrefix(strings.ToLower(tc.contentType), "image/png") {
					body = bytes.Repeat([]byte("abc "), 2000)
				}
				_, err := c.Response().Write(body)
				return err
			})
			rec := gzipGet(r, "/", "gzip")
			got := rec.Header().Get(router.HeaderContentEncoding) == "gzip"
			if got != tc.compressed {
				t.Errorf("compressed = %v, want %v", got, tc.compressed)
			}
		})
	}
}

func TestGzipSkipsACompressedTypeWithAContentLength(t *testing.T) {
	r := writerRouter(middleware.GzipConfig[*appContext]{}, func(c *appContext) error {
		c.SetHeader(router.HeaderContentType, "image/png")
		c.SetHeader(router.HeaderContentLength, "4104")
		_, err := c.Response().Write(pngBytes)
		return err
	})
	rec := gzipGet(r, "/", "gzip")
	if rec.Header().Get(router.HeaderContentEncoding) != "" || rec.Header().Get(router.HeaderContentLength) != "4104" {
		t.Errorf("headers = %v, want a plain answer that keeps its Content-Length", rec.Header())
	}
}

func TestGzipWithConfigPanicsOnAMalformedExclusion(t *testing.T) {
	for _, entry := range []string{"", "image", "/png", "image/", "*/*", "*", "image/p*", "image png", "image/png; q=1", "a/b/c"} {
		t.Run(entry, func(t *testing.T) {
			defer func() {
				msg, _ := recover().(string)
				if !strings.Contains(msg, "ExcludeContentTypes") {
					t.Errorf("recover() = %q, want a message that names ExcludeContentTypes", msg)
				}
			}()
			middleware.GzipWithConfig[*appContext](middleware.GzipConfig[*appContext]{
				ExcludeContentTypes: []string{entry},
			})
			t.Error("GzipWithConfig did not panic")
		})
	}
}

func unzlib(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	zr, err := zlib.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("read the zlib header: %v", err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read the zlib body: %v", err)
	}
	return string(body)
}

func TestGzipNegotiatesDeflate(t *testing.T) {
	tests := []struct {
		accept string
		want   string
	}{
		{"gzip", "gzip"},
		{"deflate", "deflate"},
		{"gzip, deflate", "gzip"},
		{"deflate, gzip", "gzip"},
		{"gzip;q=0.5, deflate", "deflate"},
		{"gzip, deflate;q=0.5", "gzip"},
		{"deflate;q=0.8, gzip;q=0.8", "gzip"},
		{"gzip;q=0, deflate", "deflate"},
		{"gzip, deflate;q=0", "gzip"},
		{"gzip;q=0, deflate;q=0", ""},
		{"*", "gzip"},
		{"*, gzip;q=0", "deflate"},
		{"*;q=0, deflate", "deflate"},
		{"br", ""},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.accept, func(t *testing.T) {
			r := gzipRouter(middleware.GzipConfig[*appContext]{})
			rec := gzipGet(r, "/long", tc.accept)
			if got := rec.Header().Get(router.HeaderContentEncoding); got != tc.want {
				t.Fatalf("Content-Encoding = %q, want %q", got, tc.want)
			}
			switch tc.want {
			case "gzip":
				if got := ungzip(t, rec); got != gzipLongBody {
					t.Error("the gzip body differs after a round trip")
				}
			case "deflate":
				if got := unzlib(t, rec); got != gzipLongBody {
					t.Error("the deflate body differs after a round trip")
				}
			}
			if !strings.Contains(rec.Header().Get(router.HeaderVary), router.HeaderAcceptEncoding) {
				t.Errorf("Vary = %q, want Accept-Encoding", rec.Header().Get(router.HeaderVary))
			}
		})
	}
}

func TestGzipDeflateTagsAndRevalidates(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Gzip[*appContext])
	r.GET("/", func(c *appContext) error {
		c.SetHeader(router.HeaderETag, `"abc"`)
		if c.Request().Header.Get(router.HeaderIfNoneMatch) == `"abc"` {
			return c.NoContent(http.StatusNotModified)
		}
		return c.HTML(http.StatusOK, gzipLongBody)
	})

	rec := gzipGet(r, "/", "deflate")
	if got := rec.Header().Get(router.HeaderETag); got != `"abc-deflate"` {
		t.Fatalf("ETag = %q, want the deflate suffix", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderAcceptEncoding, "deflate")
	req.Header.Set(router.HeaderIfNoneMatch, `"abc-deflate"`)
	rec = do(r, req)
	if rec.Code != http.StatusNotModified || rec.Header().Get(router.HeaderETag) != `"abc-deflate"` {
		t.Errorf("got %d with ETag %q, want a 304 with the deflate tag", rec.Code, rec.Header().Get(router.HeaderETag))
	}

	// A gzip suffix means nothing to a client that takes deflate.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderAcceptEncoding, "deflate")
	req.Header.Set(router.HeaderIfNoneMatch, `"abc-gzip"`)
	if rec = do(r, req); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for a tag of another coding", rec.Code)
	}
}

func TestGzipHeadOfDeflate(t *testing.T) {
	r := gzipRouter(middleware.GzipConfig[*appContext]{})
	req := httptest.NewRequest(http.MethodHead, "/long", nil)
	req.Header.Set(router.HeaderAcceptEncoding, "deflate")
	rec := do(r, req)
	if rec.Header().Get(router.HeaderContentEncoding) != "deflate" || rec.Body.Len() != 0 {
		t.Errorf("encoding %q with %d body bytes, want deflate and none",
			rec.Header().Get(router.HeaderContentEncoding), rec.Body.Len())
	}
}
