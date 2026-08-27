package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func requestIDRouter(cfg middleware.RequestIDConfig) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.RequestID(cfg).Middleware)
	r.GET("/", func(c *appContext) error {
		return c.String(http.StatusOK, middleware.RequestIDFrom(c))
	})
	return r
}

func TestRequestIDGeneratesAndEchoes(t *testing.T) {
	r := requestIDRouter(middleware.RequestIDConfig{})

	rec := get(r, "/")
	id := rec.Body.String()
	if id == "" {
		t.Fatal("no identifier on the context")
	}
	if got := rec.Header().Get(router.HeaderXRequestID); got != id {
		t.Errorf("header = %q, want %q", got, id)
	}
}

func TestRequestIDKeepsTheInboundValue(t *testing.T) {
	r := requestIDRouter(middleware.RequestIDConfig{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderXRequestID, "abc-123")
	if got := do(r, req).Body.String(); got != "abc-123" {
		t.Errorf("identifier = %q, want %q", got, "abc-123")
	}
}

func TestRequestIDIgnoreInbound(t *testing.T) {
	r := requestIDRouter(middleware.RequestIDConfig{IgnoreInbound: true})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderXRequestID, "abc-123")
	if got := do(r, req).Body.String(); got == "abc-123" {
		t.Error("the middleware kept a client supplied identifier")
	}
}

func TestRequestIDCustomHeaderAndGenerator(t *testing.T) {
	n := 0
	r := requestIDRouter(middleware.RequestIDConfig{
		Header:    "X-Trace",
		Generator: func() string { n++; return "trace-1" },
	})

	rec := get(r, "/")
	if got := rec.Body.String(); got != "trace-1" {
		t.Errorf("identifier = %q, want %q", got, "trace-1")
	}
	if got := rec.Header().Get("X-Trace"); got != "trace-1" {
		t.Errorf("header = %q, want %q", got, "trace-1")
	}
	if n != 1 {
		t.Errorf("the generator ran %d times, want 1", n)
	}
}

func TestRequestIDSkip(t *testing.T) {
	r := requestIDRouter(middleware.RequestIDConfig{Skip: skipPath("/")})

	if got := get(r, "/").Body.String(); got != "" {
		t.Errorf("identifier = %q, want none", got)
	}
}
