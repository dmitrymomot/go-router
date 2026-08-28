package middleware_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
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

// skipPath returns a Skip function that passes over one path.
func skipPath(path string) func(router.Context) bool {
	return func(c router.Context) bool { return c.Request().URL.Path == path }
}

// TestDefaultFactories checks that the plain factory of every middleware
// returns a usable middleware, and that the whole set composes into one chain.
//
// The factories that take an argument stay out of it: BodyLimit and RateLimit
// have no plain form to check, and MethodOverride and Rewrite belong in
// [router.Router.Pre], which runs before the route is matched.
func TestDefaultFactories(t *testing.T) {
	r := newRouter()
	r.Use(
		middleware.Recover[*appContext],
		middleware.RequestID[*appContext],
		middleware.RealIP[*appContext],
		middleware.LoggerWithConfig[*appContext](middleware.LoggerConfig{
			Logger: slog.New(slog.DiscardHandler),
		}),
		middleware.Secure[*appContext],
		middleware.CORS[*appContext],
		middleware.CSRF[*appContext],
		middleware.Decompress[*appContext],
		middleware.Gzip[*appContext],
		middleware.Timeout[*appContext],
		middleware.HTMXRedirect[*appContext],
	)
	r.GET("/", func(c *appContext) error { return c.String(http.StatusOK, "ok") })

	rec := get(r, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get(router.HeaderXRequestID) == "" {
		t.Error("RequestID did not set the header")
	}
}

// TestDefaultCORSAllowsEveryOrigin pins what the plain CORS factory does,
// because it is the one default that is permissive.
func TestDefaultCORSAllowsEveryOrigin(t *testing.T) {
	r := newRouter()
	r.Use(middleware.CORS[*appContext])
	r.GET("/data", func(c *appContext) error { return c.String(http.StatusOK, "data") })

	req := httptest.NewRequest(http.MethodGet, "/data", nil)
	req.Header.Set(router.HeaderOrigin, "https://anywhere.example")
	if got := do(r, req).Header().Get(router.HeaderAccessControlAllowOrigin); got != "*" {
		t.Errorf("allow origin = %q, want %q", got, "*")
	}
}

// TestDefaultTimeoutAppliesADeadline pins that the plain Timeout factory is
// not a no-op.
func TestDefaultTimeoutAppliesADeadline(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Timeout[*appContext])
	r.GET("/", func(c *appContext) error {
		d, ok := c.Request().Context().Deadline()
		if !ok {
			return router.ErrInternalServerError.WithMessage("no deadline")
		}
		if until := time.Until(d); until <= 0 || until > middleware.DefaultTimeout {
			return router.ErrInternalServerError.WithMessage("deadline is %s away", until)
		}
		return c.NoContent(http.StatusOK)
	})

	if rec := get(r, "/"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
}
