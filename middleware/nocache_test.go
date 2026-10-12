package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

var timeZero time.Time

func TestNoCache(t *testing.T) {
	var seen http.Header
	r := newRouter()
	r.Use(middleware.NoCache[*appContext])
	r.GET("/", func(c *appContext) error {
		seen = c.Request().Header.Clone()
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderIfNoneMatch, `"x"`)
	req.Header.Set("If-Modified-Since", "Sat, 01 Jan 2000 00:00:00 GMT")
	req.Header.Set("X-Keep", "1")
	h := do(r, req).Header()

	want := map[string]string{
		router.HeaderCacheControl: "no-cache, no-store, no-transform, must-revalidate, private, max-age=0",
		"Pragma":                  "no-cache",
		"Expires":                 "0",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if seen.Get(router.HeaderIfNoneMatch) != "" || seen.Get("If-Modified-Since") != "" {
		t.Errorf("conditional headers reached the handler: %v", seen)
	}
	if seen.Get("X-Keep") != "1" {
		t.Error("an unrelated header was dropped")
	}
}

func TestNoCacheSkip(t *testing.T) {
	r := newRouter()
	r.Use(middleware.NoCacheWithConfig(middleware.NoCacheConfig[*appContext]{Skip: skipPath("/")}))
	r.GET("/", hello)
	if get(r, "/").Header().Get(router.HeaderCacheControl) != "" {
		t.Error("a skipped request got Cache-Control")
	}
}
