package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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

	const (
		hxRequest = router.HeaderHXRequest
		hxType    = router.HeaderHXRequestType
		hxBoosted = router.HeaderHXBoosted
		hxRestore = router.HeaderHXHistoryRestoreRequest
	)
	tests := []struct {
		name     string
		headers  map[string]string
		status   int
		hx       string
		location string
	}{
		{
			name:    "a partial request gets the client-side redirect",
			headers: map[string]string{hxRequest: "true", hxType: "partial"},
			status:  http.StatusOK,
			hx:      "/there",
		},
		{
			name:     "a browser keeps the 303",
			status:   http.StatusSeeOther,
			location: "/there",
		},
		{
			name:     "a full request keeps the 303",
			headers:  map[string]string{hxRequest: "true", hxType: "full"},
			status:   http.StatusSeeOther,
			location: "/there",
		},
		{
			name:     "a full boosted request keeps the 303",
			headers:  map[string]string{hxRequest: "true", hxType: "full", hxBoosted: "true"},
			status:   http.StatusSeeOther,
			location: "/there",
		},
		{
			name:     "a full history restore keeps the 303",
			headers:  map[string]string{hxRequest: "true", hxType: "full", hxRestore: "true"},
			status:   http.StatusSeeOther,
			location: "/there",
		},
		{
			name:    "a partial boosted request gets the client-side redirect",
			headers: map[string]string{hxRequest: "true", hxType: "partial", hxBoosted: "true"},
			status:  http.StatusOK,
			hx:      "/there",
		},
		{
			name:    "a request with no type gets the client-side redirect",
			headers: map[string]string{hxRequest: "true"},
			status:  http.StatusOK,
			hx:      "/there",
		},
		{
			// htmx 2 sends no type, and it is not supported.
			name:    "an htmx 2 boosted request gets the client-side redirect",
			headers: map[string]string{hxRequest: "true", hxBoosted: "true"},
			status:  http.StatusOK,
			hx:      "/there",
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
			want := []string{hxRequest, hxType}
			if got := rec.Header().Values(router.HeaderVary); !slices.Equal(got, want) {
				t.Errorf("%s = %q, want %q", router.HeaderVary, got, want)
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

func TestHTMXRedirectKeepsTheFlashCookie(t *testing.T) {
	r := newRouter()
	r.CookieCodec(router.NewCookieCodec([]byte(strings.Repeat("k", router.MinCookieKeyLen))))
	r.Use(middleware.HTMXRedirect[*appContext])
	r.POST("/join", func(c *appContext) error {
		if err := c.AddFlash(router.Flash{Kind: "success", Message: "welcome"}); err != nil {
			return err
		}
		return c.Redirect(http.StatusSeeOther, "/chat")
	})

	req := httptest.NewRequest(http.MethodPost, "/join", nil)
	req.Header.Set(router.HeaderHXRequest, "true")
	rec := do(r, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get(router.HeaderHXRedirect); got != "/chat" {
		t.Errorf("%s = %q, want %q", router.HeaderHXRedirect, got, "/chat")
	}
	if line := rec.Header().Get("Set-Cookie"); !strings.HasPrefix(line, router.FlashCookieName+"=") {
		t.Errorf("the converted redirect carries Set-Cookie %q, want the flash cookie", line)
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

func TestHTMXRedirectVariesLikeWantsPartial(t *testing.T) {
	r := newRouter()
	r.GET("/plain", func(c *appContext) error {
		c.WantsPartial()
		return c.NoContent(http.StatusOK)
	})
	r.Group(func(r *router.Router[*appContext]) {
		r.Use(middleware.HTMXRedirect[*appContext])
		r.GET("/both", func(c *appContext) error {
			c.WantsPartial()
			return c.NoContent(http.StatusOK)
		})
	})

	headers := map[string]string{router.HeaderHXRequest: "true"}
	want := hxGet(r, "/plain", headers).Header().Values(router.HeaderVary)
	got := hxGet(r, "/both", headers).Header().Values(router.HeaderVary)
	if !slices.Equal(got, want) {
		t.Errorf("Vary behind HTMXRedirect = %q, want %q, as WantsPartial alone sets", got, want)
	}
}
