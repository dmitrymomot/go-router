package middleware_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func corsRouter(cfg middleware.CORSConfig) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.CORSWithConfig[*appContext](cfg))
	r.GET("/data", func(c *appContext) error { return c.String(http.StatusOK, "data") })
	return r
}

func preflight(target, method string, origin string) *http.Request {
	req := httptest.NewRequest(http.MethodOptions, target, nil)
	req.Header.Set(router.HeaderOrigin, origin)
	req.Header.Set(router.HeaderAccessControlRequestMethod, method)
	return req
}

func panicValue(fn func()) (v any) {
	defer func() { v = recover() }()
	fn()
	return nil
}

func TestCORSPreflight(t *testing.T) {
	r := corsRouter(middleware.CORSConfig{AllowOrigins: []string{"https://app.example"}})

	req := preflight("/data", http.MethodGet, "https://app.example")
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

func TestCORSPanicsOnTheWildcardWithCredentials(t *testing.T) {
	v := panicValue(func() {
		middleware.CORSWithConfig[*appContext](middleware.CORSConfig{
			AllowOrigins:     []string{"https://app.example", "*"},
			AllowCredentials: true,
		})
	})
	if v == nil {
		t.Fatal("no panic: every origin plus credentials built without a word")
	}
	if msg, _ := v.(string); msg == "" {
		t.Errorf("panic value = %#v, want a message", v)
	}
}

func TestCORSPanicsOnAnEntryThatIsNotAnOrigin(t *testing.T) {
	bad := []string{
		"app.example.com",
		"https://app.example.com/",
		"https://app.example.com/api",
		"https://app.example?q=1",
		"https://app.example#frag",
		"https://*.app.example",
		"https://",
		"",
		"null",
	}
	for _, o := range bad {
		t.Run(o, func(t *testing.T) {
			v := panicValue(func() {
				middleware.CORSWithConfig[*appContext](middleware.CORSConfig{AllowOrigins: []string{o}})
			})
			if v == nil {
				t.Errorf("%q built without a word, and it matches no request", o)
			}
		})
	}
}

func TestCORSTakesAnOrigin(t *testing.T) {
	good := []string{
		"https://app.example",
		"http://localhost:3000",
		"https://app.example:8443",
		"chrome-extension://abcdefghijklmnop",
		"*",
	}
	for _, o := range good {
		t.Run(o, func(t *testing.T) {
			if v := panicValue(func() {
				middleware.CORSWithConfig[*appContext](middleware.CORSConfig{AllowOrigins: []string{o}})
			}); v != nil {
				t.Errorf("%q panicked with %v, and it is an origin", o, v)
			}
		})
	}
}

func TestCORSCredentialsNameTheOrigin(t *testing.T) {
	r := corsRouter(middleware.CORSConfig{
		AllowOrigins:     []string{"https://app.example"},
		AllowCredentials: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/data", nil)
	req.Header.Set(router.HeaderOrigin, "https://app.example")
	rec := do(r, req)

	if got := rec.Header().Get(router.HeaderAccessControlAllowOrigin); got != "https://app.example" {
		t.Errorf("allow origin = %q, want the origin", got)
	}
	if got := rec.Header().Get(router.HeaderAccessControlAllowCredentials); got != "true" {
		t.Errorf("allow credentials = %q, want %q", got, "true")
	}
}

func TestCORSCopiesTheOrigins(t *testing.T) {
	origins := []string{"https://app.example"}
	r := newRouter()
	r.Use(middleware.CORSWithConfig[*appContext](middleware.CORSConfig{AllowOrigins: origins}))
	r.GET("/data", func(c *appContext) error { return c.String(http.StatusOK, "data") })
	origins[0] = "https://evil.example"

	req := httptest.NewRequest(http.MethodGet, "/data", nil)
	req.Header.Set(router.HeaderOrigin, "https://evil.example")
	if got := do(r, req).Header().Get(router.HeaderAccessControlAllowOrigin); got != "" {
		t.Errorf("allow origin = %q, want none: the caller widened the list after construction", got)
	}
}

func TestCORSAllowMethodsFollowTheRoute(t *testing.T) {
	r := corsRouter(middleware.CORSConfig{AllowOrigins: []string{"https://app.example"}})

	rec := do(r, preflight("/data", http.MethodDelete, "https://app.example"))
	if got := rec.Header().Get(router.HeaderAccessControlAllowMethods); got != "GET, HEAD, OPTIONS" {
		t.Errorf("allow methods = %q, want the methods of the route", got)
	}
}

func TestCORSAllowMethodsOverrideTheRoute(t *testing.T) {
	r := corsRouter(middleware.CORSConfig{
		AllowOrigins: []string{"https://app.example"},
		AllowMethods: []string{http.MethodGet, http.MethodPost},
	})

	rec := do(r, preflight("/data", http.MethodPost, "https://app.example"))
	if got := rec.Header().Get(router.HeaderAccessControlAllowMethods); got != "GET, POST" {
		t.Errorf("allow methods = %q, want the configured list", got)
	}
}

func TestCORSAllowMethodsFallBackToTheDefaults(t *testing.T) {
	r := newRouter()
	r.Use(middleware.CORSWithConfig[*appContext](middleware.CORSConfig{
		AllowOrigins: []string{"https://app.example"},
	}))
	r.Handle(http.MethodOptions, "/data", func(c *appContext) error {
		return c.NoContent(http.StatusNoContent)
	})

	rec := do(r, preflight("/data", http.MethodPost, "https://app.example"))
	want := "GET, HEAD, POST, PUT, PATCH, DELETE"
	if got := rec.Header().Get(router.HeaderAccessControlAllowMethods); got != want {
		t.Errorf("allow methods = %q, want %q", got, want)
	}
}

func TestCORSAllowHeadersOverrideTheAsk(t *testing.T) {
	r := corsRouter(middleware.CORSConfig{
		AllowOrigins: []string{"https://app.example"},
		AllowHeaders: []string{router.HeaderAuthorization, router.HeaderContentType},
	})

	req := preflight("/data", http.MethodGet, "https://app.example")
	req.Header.Set(router.HeaderAccessControlRequestHeaders, "X-Whatever")

	want := "Authorization, Content-Type"
	if got := do(r, req).Header().Get(router.HeaderAccessControlAllowHeaders); got != want {
		t.Errorf("allow headers = %q, want %q", got, want)
	}
}

func TestCORSExposeHeadersSkipThePreflight(t *testing.T) {
	cfg := middleware.CORSConfig{
		AllowOrigins:  []string{"https://app.example"},
		ExposeHeaders: []string{"X-Total-Count"},
	}
	r := corsRouter(cfg)

	req := httptest.NewRequest(http.MethodGet, "/data", nil)
	req.Header.Set(router.HeaderOrigin, "https://app.example")
	if got := do(r, req).Header().Get(router.HeaderAccessControlExposeHeaders); got != "X-Total-Count" {
		t.Errorf("expose headers = %q, want them on the answer that carries a body", got)
	}

	rec := do(r, preflight("/data", http.MethodGet, "https://app.example"))
	if got := rec.Header().Get(router.HeaderAccessControlExposeHeaders); got != "" {
		t.Errorf("expose headers = %q, want none: a preflight carries no body to read", got)
	}
}

func TestCORSMaxAge(t *testing.T) {
	tests := []struct {
		name string
		age  time.Duration
		want string
	}{
		{"zero sends no header", 0, ""},
		{"a duration sends its seconds", 90 * time.Minute, "5400"},
		{"a negative duration forbids caching", -time.Second, "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := corsRouter(middleware.CORSConfig{
				AllowOrigins: []string{"https://app.example"},
				MaxAge:       tt.age,
			})
			rec := do(r, preflight("/data", http.MethodGet, "https://app.example"))
			if got := rec.Header().Get(router.HeaderAccessControlMaxAge); got != tt.want {
				t.Errorf("max age = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCORSAllowOriginFuncReadsTheContext(t *testing.T) {
	r := corsRouter(middleware.CORSConfig{
		AllowOriginFunc: func(c router.Context, origin string) (bool, error) {
			return origin == "https://"+c.Host(), nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "http://tenant.example/data", nil)
	req.Header.Set(router.HeaderOrigin, "https://tenant.example")
	if got := do(r, req).Header().Get(router.HeaderAccessControlAllowOrigin); got != "https://tenant.example" {
		t.Errorf("allow origin = %q, want the origin of the host", got)
	}

	req = httptest.NewRequest(http.MethodGet, "http://tenant.example/data", nil)
	req.Header.Set(router.HeaderOrigin, "https://other.example")
	if got := do(r, req).Header().Get(router.HeaderAccessControlAllowOrigin); got != "" {
		t.Errorf("allow origin = %q, want none", got)
	}
}

func TestCORSAllowOriginFuncErrorAnswersWithoutTheHeaders(t *testing.T) {
	r := corsRouter(middleware.CORSConfig{
		AllowOriginFunc: func(router.Context, string) (bool, error) {
			return false, errors.New("the tenant store is down")
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/data", nil)
	req.Header.Set(router.HeaderOrigin, "https://app.example")
	rec := do(r, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get(router.HeaderAccessControlAllowOrigin); got != "" {
		t.Errorf("allow origin = %q, want none: nothing decided", got)
	}
}

func TestCORSWildcardStillAnswersWithTheWildcard(t *testing.T) {
	r := corsRouter(middleware.CORSConfig{AllowOrigins: []string{"*"}})

	req := httptest.NewRequest(http.MethodGet, "/data", nil)
	req.Header.Set(router.HeaderOrigin, "https://anywhere.example")
	rec := do(r, req)

	if got := rec.Header().Get(router.HeaderAccessControlAllowOrigin); got != "*" {
		t.Errorf("allow origin = %q, want %q", got, "*")
	}
	if got := rec.Header().Get(router.HeaderAccessControlAllowCredentials); got != "" {
		t.Errorf("allow credentials = %q, want none", got)
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
