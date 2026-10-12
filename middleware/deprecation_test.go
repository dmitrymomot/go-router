package middleware_test

import (
	"testing"
	"time"

	"github.com/dmitrymomot/go-router/middleware"
)

func TestDeprecation(t *testing.T) {
	dep := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sun := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	r := newRouter()
	r.Use(middleware.Deprecation(middleware.DeprecationConfig[*appContext]{
		Deprecated: dep, Sunset: sun, Link: "https://example.com/migrate",
	}))
	r.GET("/", hello)
	h := get(r, "/").Header()
	if got := h.Get("Deprecation"); got != "@1767225600" {
		t.Errorf("Deprecation = %q", got)
	}
	if got := h.Get("Sunset"); got != "Fri, 01 Jan 2027 00:00:00 GMT" {
		t.Errorf("Sunset = %q", got)
	}
	if got := h.Get("Link"); got != `<https://example.com/migrate>; rel="deprecation"` {
		t.Errorf("Link = %q", got)
	}
}

func TestDeprecationOptionalHeadersAndSkip(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Deprecation(middleware.DeprecationConfig[*appContext]{
		Deprecated: time.Unix(1, 0), Skip: skipPath("/skip"),
	}))
	r.GET("/", hello)
	r.GET("/skip", hello)
	h := get(r, "/").Header()
	if h.Get("Deprecation") != "@1" || h.Get("Sunset") != "" || h.Get("Link") != "" {
		t.Errorf("headers = %v", h)
	}
	if get(r, "/skip").Header().Get("Deprecation") != "" {
		t.Error("a skipped request got the header")
	}
}

func TestDeprecationPanics(t *testing.T) {
	now := time.Now()
	tests := map[string]middleware.DeprecationConfig[*appContext]{
		"zero":           {},
		"sunset earlier": {Deprecated: now, Sunset: now.Add(-time.Hour)},
		"bad link":       {Deprecated: now, Link: "https://x/>; rel=evil"},
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("did not panic")
				}
			}()
			middleware.Deprecation(cfg)
		})
	}
}
