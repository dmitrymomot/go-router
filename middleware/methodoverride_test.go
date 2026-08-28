package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// methodOverrideRouter answers every method on one path with the name of the
// method that reached it.
func methodOverrideRouter(cfg middleware.MethodOverrideConfig) *router.Router[*appContext] {
	r := newRouter()
	r.Pre(middleware.MethodOverrideWithConfig[*appContext](cfg))
	echo := func(c *appContext) error { return c.String(http.StatusOK, c.Request().Method) }
	r.GET("/posts/{id}", echo)
	r.POST("/posts/{id}", echo)
	r.PUT("/posts/{id}", echo)
	r.PATCH("/posts/{id}", echo)
	r.DELETE("/posts/{id}", echo)
	return r
}

// methodOverridePost builds a POST that names another method in the header.
func methodOverridePost(target, override string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, nil)
	if override != "" {
		req.Header.Set(router.HeaderXHTTPMethodOverride, override)
	}
	return req
}

func TestMethodOverrideUpgradesAPost(t *testing.T) {
	tests := []struct {
		name     string
		override string
		want     string
	}{
		{name: "delete", override: "DELETE", want: http.MethodDelete},
		{name: "put", override: "PUT", want: http.MethodPut},
		{name: "patch", override: "PATCH", want: http.MethodPatch},
		{name: "in lower case", override: "delete", want: http.MethodDelete},
		{name: "with spaces around it", override: " delete ", want: http.MethodDelete},
		{name: "a downgrade to get", override: "GET", want: http.MethodPost},
		{name: "a downgrade to head", override: "HEAD", want: http.MethodPost},
		{name: "a method that is not one", override: "TELEPORT", want: http.MethodPost},
		{name: "no header at all", override: "", want: http.MethodPost},
	}

	r := methodOverrideRouter(middleware.MethodOverrideConfig{})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(r, methodOverridePost("/posts/7", tc.override))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if rec.Body.String() != tc.want {
				t.Errorf("method = %q, want %q", rec.Body.String(), tc.want)
			}
		})
	}
}

func TestMethodOverrideIgnoresEveryOtherMethod(t *testing.T) {
	r := methodOverrideRouter(middleware.MethodOverrideConfig{})

	req := httptest.NewRequest(http.MethodGet, "/posts/7", nil)
	req.Header.Set(router.HeaderXHTTPMethodOverride, "DELETE")
	if got := do(r, req).Body.String(); got != http.MethodGet {
		t.Errorf("method = %q, want GET: only a POST is upgraded", got)
	}
}

func TestMethodOverridePlainFormReadsTheHeader(t *testing.T) {
	r := newRouter()
	r.Pre(middleware.MethodOverride[*appContext])
	r.DELETE("/posts/{id}", func(c *appContext) error {
		return c.String(http.StatusOK, c.Param("id"))
	})

	rec := do(r, methodOverridePost("/posts/7", "DELETE"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "7" {
		t.Errorf("id = %q, want 7", rec.Body.String())
	}
}

func TestMethodOverrideFromFormLeavesTheBodyReadable(t *testing.T) {
	r := newRouter()
	r.Pre(middleware.MethodOverrideWithConfig[*appContext](middleware.MethodOverrideConfig{
		Getter: middleware.MethodFromForm("_method"),
	}))
	r.DELETE("/posts/{id}", func(c *appContext) error {
		return c.Stringf(http.StatusOK, "%s %s", c.Request().Method, c.FormValue("title"))
	})

	req := httptest.NewRequest(http.MethodPost, "/posts/7",
		strings.NewReader("_method=DELETE&title=the+title"))
	req.Header.Set(router.HeaderContentType, router.MIMEApplicationForm)

	rec := do(r, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != "DELETE the title" {
		t.Errorf("the handler saw %q, want the method and the field beside it", got)
	}
}

func TestMethodOverrideFromQuery(t *testing.T) {
	r := methodOverrideRouter(middleware.MethodOverrideConfig{
		Getter: middleware.MethodFromQuery("_method"),
	})

	req := httptest.NewRequest(http.MethodPost, "/posts/7?_method=PATCH", nil)
	if got := do(r, req).Body.String(); got != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", got)
	}
}

func TestMethodOverrideFromHeaderReadsTheNamedHeader(t *testing.T) {
	r := methodOverrideRouter(middleware.MethodOverrideConfig{
		Getter: middleware.MethodFromHeader("X-Method"),
	})

	req := httptest.NewRequest(http.MethodPost, "/posts/7", nil)
	req.Header.Set("X-Method", "PUT")
	req.Header.Set(router.HeaderXHTTPMethodOverride, "DELETE")
	if got := do(r, req).Body.String(); got != http.MethodPut {
		t.Errorf("method = %q, want PUT: the configured header decides", got)
	}
}

func TestMethodOverrideSkip(t *testing.T) {
	r := methodOverrideRouter(middleware.MethodOverrideConfig{
		Skip: skipPath("/posts/7"),
	})

	if got := do(r, methodOverridePost("/posts/7", "DELETE")).Body.String(); got != http.MethodPost {
		t.Errorf("method = %q, want POST", got)
	}
}
