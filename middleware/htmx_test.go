package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func hxGet(h http.Handler, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return do(h, req)
}

func redirectRouter(mw router.Middleware[*appContext]) *router.Router[*appContext] {
	r := newRouter()
	r.Use(mw)
	r.GET("/go", func(c *appContext) error {
		return c.Redirect(http.StatusSeeOther, "/there")
	})
	return r
}

func TestHTMXRedirect(t *testing.T) {
	r := redirectRouter(middleware.HTMXRedirect[*appContext])

	tests := []struct {
		name     string
		headers  map[string]string
		status   int
		hx       string
		location string
	}{
		{
			name:    "htmx gets the client-side redirect",
			headers: map[string]string{router.HeaderHXRequest: "true"},
			status:  http.StatusOK,
			hx:      "/there",
		},
		{
			name:     "a browser keeps the 303",
			status:   http.StatusSeeOther,
			location: "/there",
		},
		{
			name: "a boosted request keeps the 303",
			headers: map[string]string{
				router.HeaderHXRequest: "true",
				router.HeaderHXBoosted: "true",
			},
			status:   http.StatusSeeOther,
			location: "/there",
		},
		{
			name: "a history restore keeps the 303",
			headers: map[string]string{
				router.HeaderHXRequest:               "true",
				router.HeaderHXHistoryRestoreRequest: "true",
			},
			status:   http.StatusSeeOther,
			location: "/there",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := hxGet(r, "/go", tc.headers)
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
			if got := rec.Header().Get(router.HeaderHXRedirect); got != tc.hx {
				t.Errorf("%s = %q, want %q", router.HeaderHXRedirect, got, tc.hx)
			}
			if got := rec.Header().Get(router.HeaderLocation); got != tc.location {
				t.Errorf("%s = %q, want %q", router.HeaderLocation, got, tc.location)
			}
			if got := rec.Header().Get(router.HeaderVary); got != router.HeaderHXRequest {
				t.Errorf("%s = %q, want %q", router.HeaderVary, got, router.HeaderHXRequest)
			}
		})
	}
}

func TestHTMXRedirectLocationConfig(t *testing.T) {
	r := redirectRouter(middleware.HTMXRedirectWithConfig[*appContext](
		middleware.HTMXRedirectConfig{Location: true}))

	rec := hxGet(r, "/go", map[string]string{router.HeaderHXRequest: "true"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get(router.HeaderHXLocation); got != "/there" {
		t.Errorf("%s = %q, want %q", router.HeaderHXLocation, got, "/there")
	}
	if got := rec.Header().Get(router.HeaderHXRedirect); got != "" {
		t.Errorf("%s = %q, want no header", router.HeaderHXRedirect, got)
	}
}

func TestHTMXRedirectSkip(t *testing.T) {
	r := redirectRouter(middleware.HTMXRedirectWithConfig[*appContext](
		middleware.HTMXRedirectConfig{Skip: skipPath("/go")}))

	rec := hxGet(r, "/go", map[string]string{router.HeaderHXRequest: "true"})
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get(router.HeaderVary); got != "" {
		t.Errorf("%s = %q, want no header", router.HeaderVary, got)
	}
}

func TestHTMXRedirectLeavesEveryOtherAnswerAlone(t *testing.T) {
	r := newRouter()
	r.Use(middleware.HTMXRedirect[*appContext])
	r.GET("/ok", func(c *appContext) error { return c.String(http.StatusOK, "ok") })
	r.GET("/fresh", func(c *appContext) error { return c.NoContent(http.StatusNotModified) })
	r.GET("/gone", func(c *appContext) error { return router.ErrNotFound })

	tests := []struct {
		target string
		status int
	}{
		{"/ok", http.StatusOK},
		{"/fresh", http.StatusNotModified},
		{"/gone", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			rec := hxGet(r, tc.target, map[string]string{router.HeaderHXRequest: "true"})
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
			if got := rec.Header().Get(router.HeaderHXRedirect); got != "" {
				t.Errorf("%s = %q, want no header", router.HeaderHXRedirect, got)
			}
		})
	}
}

func TestHTMXRedirectReportsTheStatusThatWentOut(t *testing.T) {
	var logged int
	watch := func(next router.HandlerFunc[*appContext]) router.HandlerFunc[*appContext] {
		return func(c *appContext) error {
			err := next(c)
			logged = c.Response().Status
			return err
		}
	}

	r := newRouter()
	r.Use(watch, middleware.HTMXRedirect[*appContext])
	r.GET("/go", func(c *appContext) error { return c.Redirect(http.StatusSeeOther, "/there") })

	hxGet(r, "/go", map[string]string{router.HeaderHXRequest: "true"})
	if logged != http.StatusOK {
		t.Errorf("the recorded status = %d, want 200", logged)
	}
}

func TestHTMXRedirectComposesWithHX(t *testing.T) {
	r := newRouter()
	r.Use(middleware.HTMXRedirect[*appContext])
	r.POST("/join", func(c *appContext) error { return c.HX().Redirect("/chat") })

	req := httptest.NewRequest(http.MethodPost, "/join", nil)
	req.Header.Set(router.HeaderHXRequest, "true")
	rec := do(r, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get(router.HeaderHXRedirect); got != "/chat" {
		t.Errorf("%s = %q, want %q", router.HeaderHXRedirect, got, "/chat")
	}
}

func TestHTMXRedirectKeepsTheStreamFlushable(t *testing.T) {
	r := newRouter()
	r.Use(middleware.HTMXRedirect[*appContext])
	r.GET("/events", func(c *appContext) error {
		s, err := c.SSE(http.StatusOK)
		if err != nil {
			return err
		}
		return s.Send(router.Event{Name: "tick", Data: "one"})
	})

	rec := hxGet(r, "/events", map[string]string{router.HeaderHXRequest: "true"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	want := "event: tick\ndata: one\n\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}
