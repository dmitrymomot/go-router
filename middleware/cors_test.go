package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func corsRouter(cfg middleware.CORSConfig) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.CORSWithConfig[*appContext](cfg))
	r.GET("/data", func(c *appContext) error { return c.String(http.StatusOK, "data") })
	return r
}

func TestCORSPreflight(t *testing.T) {
	r := corsRouter(middleware.CORSConfig{AllowOrigins: []string{"https://app.example"}})

	req := httptest.NewRequest(http.MethodOptions, "/data", nil)
	req.Header.Set(router.HeaderOrigin, "https://app.example")
	req.Header.Set(router.HeaderAccessControlRequestMethod, http.MethodGet)
	req.Header.Set(router.HeaderAccessControlRequestHeaders, "Authorization")

	rec := do(r, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get(router.HeaderAccessControlAllowOrigin); got != "https://app.example" {
		t.Errorf("allow origin = %q", got)
	}
	if got := rec.Header().Get(router.HeaderAccessControlAllowHeaders); got != "Authorization" {
		t.Errorf("allow headers = %q, want the headers that the preflight asked for", got)
	}
}

func TestCORSRejectsAnUnknownOrigin(t *testing.T) {
	r := corsRouter(middleware.CORSConfig{AllowOrigins: []string{"https://app.example"}})

	req := httptest.NewRequest(http.MethodGet, "/data", nil)
	req.Header.Set(router.HeaderOrigin, "https://evil.example")
	rec := do(r, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get(router.HeaderAccessControlAllowOrigin); got != "" {
		t.Errorf("allow origin = %q, want none", got)
	}
}

func TestCORSCredentialsEchoTheOrigin(t *testing.T) {
	r := corsRouter(middleware.CORSConfig{
		AllowOrigins:     []string{"*"},
		AllowCredentials: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/data", nil)
	req.Header.Set(router.HeaderOrigin, "https://app.example")
	rec := do(r, req)

	// "*" and credentials cannot combine, so the answer names the origin.
	if got := rec.Header().Get(router.HeaderAccessControlAllowOrigin); got != "https://app.example" {
		t.Errorf("allow origin = %q, want the request origin", got)
	}
}

func TestCORSSkip(t *testing.T) {
	r := corsRouter(middleware.CORSConfig{
		AllowOrigins: []string{"*"},
		Skip:         skipPath("/data"),
	})

	req := httptest.NewRequest(http.MethodGet, "/data", nil)
	req.Header.Set(router.HeaderOrigin, "https://app.example")
	if got := do(r, req).Header().Get(router.HeaderAccessControlAllowOrigin); got != "" {
		t.Errorf("allow origin = %q, want none", got)
	}
}
