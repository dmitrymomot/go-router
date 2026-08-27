package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func realIPRouter(cfg middleware.RealIPConfig) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.RealIPWithConfig[*appContext](cfg))
	r.GET("/", func(c *appContext) error {
		return c.String(http.StatusOK, middleware.ClientIP(c))
	})
	return r
}

func TestRealIP(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderXForwardedFor, "203.0.113.7, 10.0.0.1")
	if got := do(r, req).Body.String(); got != "203.0.113.7" {
		t.Errorf("client ip = %q, want %q", got, "203.0.113.7")
	}

	// X-Real-Ip comes first by default.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderXForwardedFor, "203.0.113.7")
	req.Header.Set(router.HeaderXRealIP, "198.51.100.4")
	if got := do(r, req).Body.String(); got != "198.51.100.4" {
		t.Errorf("client ip = %q, want %q", got, "198.51.100.4")
	}

	// Without the headers the remote address of the connection stands.
	if got := get(r, "/").Body.String(); got != "192.0.2.1" {
		t.Errorf("client ip = %q, want %q", got, "192.0.2.1")
	}
}

func TestRealIPCustomHeaders(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{Headers: []string{"Cf-Connecting-Ip"}})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Cf-Connecting-Ip", "203.0.113.9")
	req.Header.Set(router.HeaderXForwardedFor, "10.0.0.1")
	if got := do(r, req).Body.String(); got != "203.0.113.9" {
		t.Errorf("client ip = %q, want %q", got, "203.0.113.9")
	}
}

func TestRealIPSkip(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{Skip: skipPath("/")})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderXForwardedFor, "203.0.113.7")
	if got := do(r, req).Body.String(); got != "192.0.2.1" {
		t.Errorf("client ip = %q, want the connection address", got)
	}
}
