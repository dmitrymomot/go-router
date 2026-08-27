package middleware_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// appContext is the application context of the tests. It shows the shape that
// an application uses: embed router.Base and add fields.
type appContext struct {
	router.Base
	Tenant string
}

func newRouter() *router.Router[*appContext] {
	return router.New(func(http.ResponseWriter, *http.Request) *appContext {
		return &appContext{Tenant: "acme"}
	})
}

func do(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func get(h http.Handler, target string) *httptest.ResponseRecorder {
	return do(h, httptest.NewRequest(http.MethodGet, target, nil))
}

func TestRecoverTurnsAPanicIntoA500(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Recover)
	r.GET("/boom", func(*appContext) error { panic("handler exploded") })

	rec := get(r, "/boom")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "exploded") {
		t.Errorf("the body leaks the panic value: %q", rec.Body.String())
	}
}

func TestRecoverPassesOnErrAbortHandler(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Recover)
	r.GET("/abort", func(*appContext) error { panic(http.ErrAbortHandler) })

	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", rec)
		}
	}()
	get(r, "/abort")
}

func TestRequestID(t *testing.T) {
	r := newRouter()
	r.Use(middleware.RequestID)
	r.GET("/", func(c *appContext) error {
		return c.String(http.StatusOK, middleware.RequestIDFrom(c))
	})

	rec := get(r, "/")
	id := rec.Body.String()
	if id == "" {
		t.Fatal("no identifier on the context")
	}
	if got := rec.Header().Get(router.HeaderXRequestID); got != id {
		t.Errorf("header = %q, want %q", got, id)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderXRequestID, "abc-123")
	if got := do(r, req).Body.String(); got != "abc-123" {
		t.Errorf("inbound identifier = %q, want %q", got, "abc-123")
	}
}

func TestRealIP(t *testing.T) {
	r := newRouter()
	r.Use(middleware.RealIP)
	r.GET("/", func(c *appContext) error {
		return c.String(http.StatusOK, middleware.ClientIP(c))
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderXForwardedFor, "203.0.113.7, 10.0.0.1")
	if got := do(r, req).Body.String(); got != "203.0.113.7" {
		t.Errorf("client ip = %q, want %q", got, "203.0.113.7")
	}

	// Without the headers the remote address of the connection stands.
	if got := get(r, "/").Body.String(); got != "192.0.2.1" {
		t.Errorf("client ip = %q, want %q", got, "192.0.2.1")
	}
}

func TestLoggerReportsTheStatusOfAFailedHandler(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	r := newRouter()
	r.Use(middleware.Logger(log).Middleware)
	r.GET("/gone", func(*appContext) error { return router.ErrGone })
	r.GET("/ok", func(c *appContext) error { return c.String(http.StatusOK, "fine") })
	r.GET("/health", func(c *appContext) error { return c.NoContent(http.StatusNoContent) })

	get(r, "/gone")
	// The handler returned an error, so the response was still uncommitted
	// when the middleware ran. The record must still carry 410.
	if !strings.Contains(buf.String(), "status=410") {
		t.Errorf("record = %q, want status=410", buf.String())
	}
	if !strings.Contains(buf.String(), "route=/gone") {
		t.Errorf("record = %q, want the route pattern", buf.String())
	}

	buf.Reset()
	get(r, "/ok")
	if !strings.Contains(buf.String(), "status=200") {
		t.Errorf("record = %q, want status=200", buf.String())
	}
}

func TestLoggerSkip(t *testing.T) {
	var buf bytes.Buffer
	cfg := middleware.Logger(slog.New(slog.NewTextHandler(&buf, nil)))
	cfg.Skip = func(path string) bool { return path == "/health" }

	r := newRouter()
	r.Use(cfg.Middleware)
	r.GET("/health", func(c *appContext) error { return c.NoContent(http.StatusNoContent) })

	get(r, "/health")
	if buf.Len() != 0 {
		t.Errorf("skipped path still logged: %q", buf.String())
	}
}

func TestCORSPreflight(t *testing.T) {
	r := newRouter()
	r.Use(middleware.CORS("https://app.example").Middleware)
	r.GET("/data", func(c *appContext) error { return c.String(http.StatusOK, "data") })

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
	r := newRouter()
	r.Use(middleware.CORS("https://app.example").Middleware)
	r.GET("/data", func(c *appContext) error { return c.String(http.StatusOK, "data") })

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
	cfg := middleware.CORS("*")
	cfg.AllowCredentials = true

	r := newRouter()
	r.Use(cfg.Middleware)
	r.GET("/data", func(c *appContext) error { return c.String(http.StatusOK, "data") })

	req := httptest.NewRequest(http.MethodGet, "/data", nil)
	req.Header.Set(router.HeaderOrigin, "https://app.example")
	rec := do(r, req)

	// "*" and credentials cannot combine, so the answer names the origin.
	if got := rec.Header().Get(router.HeaderAccessControlAllowOrigin); got != "https://app.example" {
		t.Errorf("allow origin = %q, want the request origin", got)
	}
}

func TestTimeout(t *testing.T) {
	// synctest runs the deadline on a fake clock, so the test finishes at once
	// and never depends on the speed of the machine.
	synctest.Test(t, func(t *testing.T) {
		r := newRouter()
		r.Use(middleware.Timeout(20 * time.Millisecond).Middleware)
		r.GET("/slow", func(c *appContext) error {
			<-c.Done()
			return c.Err()
		})
		r.GET("/fast", func(c *appContext) error { return c.String(http.StatusOK, "fast") })

		if rec := get(r, "/slow"); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
		if rec := get(r, "/fast"); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})
}

func TestTimeoutPassesTheDeadlineToTheHandler(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Timeout(time.Minute).Middleware)
	r.GET("/", func(c *appContext) error {
		if _, ok := c.Request().Context().Deadline(); !ok {
			return router.ErrInternalServerError.WithMessage("no deadline")
		}
		var _ context.Context = c
		return c.NoContent(http.StatusOK)
	})

	if rec := get(r, "/"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
}
