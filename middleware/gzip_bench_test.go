package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// discardResponse drops the body, so a benchmark measures the middleware and
// not the recorder.
type discardResponse struct{ h http.Header }

func (d *discardResponse) Header() http.Header         { return d.h }
func (d *discardResponse) Write(p []byte) (int, error) { return len(p), nil }
func (d *discardResponse) WriteHeader(int)             {}

func benchmarkGzipWrites(b *testing.B, size, chunk int) {
	body := []byte(strings.Repeat("<p>the quick brown fox</p>", size/26+1))[:size]
	r := newRouter()
	r.Use(middleware.Gzip[*appContext])
	r.GET("/", func(c *appContext) error {
		c.SetHeader(router.HeaderContentType, "text/html; charset=utf-8")
		for p := body; len(p) > 0; {
			n := min(chunk, len(p))
			if _, err := c.Response().Write(p[:n]); err != nil {
				return err
			}
			p = p[n:]
		}
		return nil
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderAcceptEncoding, "gzip")
	b.ReportAllocs()
	b.SetBytes(int64(size))
	for b.Loop() {
		r.ServeHTTP(&discardResponse{h: http.Header{}}, req)
	}
}

func BenchmarkGzipOneLargeWrite(b *testing.B) { benchmarkGzipWrites(b, 1<<20, 1<<20) }
func BenchmarkGzipChunkedWrites(b *testing.B) { benchmarkGzipWrites(b, 1<<20, 4<<10) }
func BenchmarkGzipSmallBody(b *testing.B)     { benchmarkGzipWrites(b, 4<<10, 4<<10) }
