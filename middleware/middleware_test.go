package middleware_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// TestConstructorsTakeAtMostOneConfig pins the variadic contract: no argument
// means the defaults, one argument configures, and more is a mistake that the
// middleware refuses to guess about.
func TestConstructorsTakeAtMostOneConfig(t *testing.T) {
	tests := []struct {
		name string
		call func()
	}{
		{"Recover", func() { middleware.Recover(middleware.RecoverConfig{}, middleware.RecoverConfig{}) }},
		{"RequestID", func() { middleware.RequestID(middleware.RequestIDConfig{}, middleware.RequestIDConfig{}) }},
		{"RealIP", func() { middleware.RealIP(middleware.RealIPConfig{}, middleware.RealIPConfig{}) }},
		{"Logger", func() { middleware.Logger(middleware.LoggerConfig{}, middleware.LoggerConfig{}) }},
		{"CORS", func() { middleware.CORS(middleware.CORSConfig{}, middleware.CORSConfig{}) }},
		{"Timeout", func() { middleware.Timeout(middleware.TimeoutConfig{}, middleware.TimeoutConfig{}) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				msg, _ := recover().(string)
				if !strings.Contains(msg, "at most one config") {
					t.Errorf("panic = %q, want one about the config count", msg)
				}
			}()
			tc.call()
		})
	}
}

// TestDefaultsWithoutAConfig checks that every constructor is usable with no
// argument at all.
func TestDefaultsWithoutAConfig(t *testing.T) {
	r := newRouter()
	r.Use(
		middleware.Recover().Middleware,
		middleware.RequestID().Middleware,
		middleware.RealIP().Middleware,
		middleware.Logger(middleware.LoggerConfig{Logger: slog.New(slog.DiscardHandler)}).Middleware,
		middleware.CORS().Middleware,
		middleware.Timeout().Middleware,
	)
	r.GET("/", func(c *appContext) error { return c.String(http.StatusOK, "ok") })

	if rec := get(r, "/"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}
