package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func crossOriginRouter(cfg middleware.CrossOriginConfig[*appContext]) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.CrossOriginWithConfig[*appContext](cfg))
	h := func(c *appContext) error { return c.String(http.StatusOK, "ok") }
	r.GET("/", h)
	r.POST("/", h)
	r.POST("/hook", h)
	return r
}

func TestCrossOrigin(t *testing.T) {
	r := crossOriginRouter(middleware.CrossOriginConfig[*appContext]{
		TrustedOrigins: []string{"https://partner.example:443"},
	})
	tests := []struct {
		name    string
		method  string
		headers map[string]string
		want    int
	}{
		{"a form from the same origin", http.MethodPost, map[string]string{router.HeaderSecFetchSite: "same-origin"}, http.StatusOK},
		{"a navigation by the user", http.MethodPost, map[string]string{router.HeaderSecFetchSite: "none"}, http.StatusOK},
		{"a form from another site", http.MethodPost, map[string]string{router.HeaderSecFetchSite: "cross-site"}, http.StatusForbidden},
		{"a form from a sibling subdomain", http.MethodPost, map[string]string{router.HeaderSecFetchSite: "same-site"}, http.StatusForbidden},
		{"a trusted origin", http.MethodPost, map[string]string{
			router.HeaderSecFetchSite: "cross-site", router.HeaderOrigin: "https://partner.example",
		}, http.StatusOK},
		{"another origin", http.MethodPost, map[string]string{
			router.HeaderSecFetchSite: "cross-site", router.HeaderOrigin: "https://evil.example",
		}, http.StatusForbidden},
		{"no header at all", http.MethodPost, nil, http.StatusOK},
		{"a safe method from another site", http.MethodGet, map[string]string{router.HeaderSecFetchSite: "cross-site"}, http.StatusOK},
		{"an origin of another host and no fetch metadata", http.MethodPost, map[string]string{router.HeaderOrigin: "https://evil.example"}, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rec := do(r, req)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
			if got := rec.Result().Cookies(); len(got) != 0 {
				t.Errorf("the answer set %d cookies, want none", len(got))
			}
		})
	}
}

func TestCrossOriginSkip(t *testing.T) {
	r := crossOriginRouter(middleware.CrossOriginConfig[*appContext]{Skip: skipPath("/hook")})
	for target, want := range map[string]int{"/hook": http.StatusOK, "/": http.StatusForbidden} {
		req := httptest.NewRequest(http.MethodPost, target, nil)
		req.Header.Set(router.HeaderSecFetchSite, "cross-site")
		if rec := do(r, req); rec.Code != want {
			t.Errorf("%s: status = %d, want %d", target, rec.Code, want)
		}
	}
}

func TestCrossOriginOnDeny(t *testing.T) {
	var reason error
	r := crossOriginRouter(middleware.CrossOriginConfig[*appContext]{
		OnDeny: func(c *appContext, err error) error {
			reason = err
			return router.ErrConflict.WithMessage("no")
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set(router.HeaderSecFetchSite, "cross-site")
	if rec := do(r, req); rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if reason == nil {
		t.Error("OnDeny got no reason")
	}

	r = crossOriginRouter(middleware.CrossOriginConfig[*appContext]{
		OnDeny: func(c *appContext, _ error) error {
			return c.String(http.StatusConflict, "handled")
		},
	})
	rec := do(r, req.Clone(req.Context()))
	if rec.Code != http.StatusConflict || rec.Body.String() != "handled" {
		t.Errorf("got %d %q, want the answer that OnDeny wrote", rec.Code, rec.Body.String())
	}
}

func TestCrossOriginDefaultAnswer(t *testing.T) {
	r := crossOriginRouter(middleware.CrossOriginConfig[*appContext]{})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set(router.HeaderSecFetchSite, "cross-site")
	rec := do(r, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "cross-origin request") {
		t.Errorf("got %d %q, want a 403 that names the cross-origin request", rec.Code, rec.Body.String())
	}
}

func TestCrossOriginMalformedTrustedOriginPanics(t *testing.T) {
	for _, origin := range []string{"app.example", "https://app.example/path", "*", ""} {
		t.Run(origin, func(t *testing.T) {
			mustPanicContaining(t, "CrossOriginConfig.TrustedOrigins", func() {
				middleware.CrossOriginWithConfig[*appContext](middleware.CrossOriginConfig[*appContext]{
					TrustedOrigins: []string{origin},
				})
			})
		})
	}
}
