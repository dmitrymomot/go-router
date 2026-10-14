package i18n_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/i18n"
)

type tctx struct {
	router.Base
	userLocale string
}

func newRouter() *router.Router[*tctx] {
	return router.New(func(http.ResponseWriter, *http.Request) *tctx { return new(tctx) })
}

func webBundle(t *testing.T) *i18n.Bundle {
	t.Helper()
	b, err := i18n.New("en", i18n.WithMessages(fstest.MapFS{
		"en/app.json":    {Data: []byte(`{"hi": "Hello", "items": {"one": "{{count}} item", "other": "{{count}} items"}}`)},
		"uk/app.json":    {Data: []byte(`{"hi": "Привіт"}`)},
		"en-GB/app.json": {Data: []byte(`{"hi": "Hiya"}`)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func serve(h http.Handler, target string, header map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func hiHandler(c *tctx) error {
	return c.String(http.StatusOK, i18n.T(c, "app.hi"))
}

func TestMiddlewareDefaultsToAcceptLanguage(t *testing.T) {
	r := newRouter()
	r.Use(i18n.Middleware[*tctx](webBundle(t)))
	r.GET("/", hiHandler)

	for _, tc := range []struct{ accept, body, lang string }{
		{"uk, en;q=0.5", "Привіт", "uk"},
		{"en-GB", "Hiya", "en-GB"},
		{"en-AU", "Hello", "en"},
		{"fr", "Hello", "en"},
		{"", "Hello", "en"},
	} {
		rec := serve(r, "/", map[string]string{"Accept-Language": tc.accept})
		if got := rec.Body.String(); got != tc.body {
			t.Errorf("Accept-Language %q: body = %q, want %q", tc.accept, got, tc.body)
		}
		if got := rec.Header().Get("Content-Language"); got != tc.lang {
			t.Errorf("Accept-Language %q: Content-Language = %q, want %q", tc.accept, got, tc.lang)
		}
		if got := rec.Header().Values("Vary"); !slices.Equal(got, []string{"Accept-Language"}) {
			t.Errorf("Vary = %q, want [Accept-Language]", got)
		}
	}
}

func TestMiddlewareSourcesInOrder(t *testing.T) {
	r := newRouter()
	r.Use(func(next router.HandlerFunc[*tctx]) router.HandlerFunc[*tctx] {
		return func(c *tctx) error {
			c.userLocale = c.Request().Header.Get("X-User")
			return next(c)
		}
	})
	r.Use(i18n.MiddlewareWithConfig(i18n.Config[*tctx]{
		Bundle: webBundle(t),
		Sources: []i18n.Source{
			i18n.FromParam("lang"),
			i18n.FromFunc("x-user", func(c *tctx) (string, bool) { return c.userLocale, c.userLocale != "" }),
			i18n.FromCookie("lang"),
			i18n.FromQuery("lang"),
			i18n.FromHeader("x-locale"),
			i18n.FromAcceptLanguage(),
			i18n.FromCookie("lang"), // a repeat adds no second Vary
		},
	}))
	r.GET("/", hiHandler)
	r.GET("/p/{lang}", hiHandler)

	for _, tc := range []struct {
		name, target string
		header       map[string]string
		cookie       *http.Cookie
		want         string
	}{
		{"param", "/p/uk", map[string]string{"X-User": "en", "Accept-Language": "en"}, nil, "Привіт"},
		{"param unknown passes", "/p/fr", map[string]string{"X-User": "uk"}, nil, "Привіт"},
		{"func", "/", map[string]string{"X-User": "uk"}, &http.Cookie{Name: "lang", Value: "en"}, "Привіт"},
		{"func unknown passes", "/", map[string]string{"X-User": "fr"}, &http.Cookie{Name: "lang", Value: "uk"}, "Привіт"},
		{"cookie", "/?lang=en", nil, &http.Cookie{Name: "lang", Value: "uk"}, "Привіт"},
		{"query", "/?lang=uk", map[string]string{"X-Locale": "en"}, nil, "Привіт"},
		{"header", "/", map[string]string{"X-Locale": "uk", "Accept-Language": "en"}, nil, "Привіт"},
		{"accept", "/", map[string]string{"Accept-Language": "uk"}, nil, "Привіт"},
		{"default", "/?lang=", map[string]string{"X-Locale": "zz"}, nil, "Hello"},
	} {
		var cookies []*http.Cookie
		if tc.cookie != nil {
			cookies = append(cookies, tc.cookie)
		}
		rec := serve(r, tc.target, tc.header, cookies...)
		if got := rec.Body.String(); got != tc.want {
			t.Errorf("%s: body = %q, want %q", tc.name, got, tc.want)
		}
		want := []string{"X-User", "Cookie", "X-Locale", "Accept-Language"}
		if got := rec.Header().Values("Vary"); !slices.Equal(got, want) {
			t.Errorf("%s: Vary = %q, want %q", tc.name, got, want)
		}
	}
}

func TestMiddlewareSkip(t *testing.T) {
	r := newRouter()
	r.Use(i18n.MiddlewareWithConfig(i18n.Config[*tctx]{
		Bundle: webBundle(t),
		Skip:   func(c *tctx) bool { return c.Request().URL.Path == "/health" },
	}))
	r.GET("/health", func(c *tctx) error {
		if !i18n.From(c).IsZero() {
			t.Error("a skipped request carries a Localizer")
		}
		return hiHandler(c)
	})

	rec := serve(r, "/health", map[string]string{"Accept-Language": "uk"})
	if got := rec.Body.String(); got != "app.hi" {
		t.Errorf("body = %q, want the key echoed", got)
	}
	if got := rec.Header().Get("Content-Language"); got != "" {
		t.Errorf("Content-Language = %q on a skipped request", got)
	}
}

func TestMiddlewareKeepsVary(t *testing.T) {
	r := newRouter()
	r.Use(func(next router.HandlerFunc[*tctx]) router.HandlerFunc[*tctx] {
		return func(c *tctx) error {
			c.Response().Header().Add("Vary", "Origin")
			return next(c)
		}
	})
	r.Use(i18n.Middleware[*tctx](webBundle(t)))
	r.GET("/", hiHandler)

	rec := serve(r, "/", nil)
	if got := rec.Header().Values("Vary"); !slices.Equal(got, []string{"Origin", "Accept-Language"}) {
		t.Errorf("Vary = %q", got)
	}
}

func TestMiddlewareReachesComponents(t *testing.T) {
	r := newRouter()
	r.Use(i18n.Middleware[*tctx](webBundle(t)))
	r.GET("/", func(c *tctx) error {
		return c.Render(http.StatusOK, router.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			_, err := io.WriteString(w, i18n.TN(ctx, "app.items", 3))
			return err
		}))
	})

	rec := serve(r, "/", nil)
	if got := rec.Body.String(); got != "3 items" {
		t.Errorf("body = %q, want %q", got, "3 items")
	}
}

func TestMiddlewarePanics(t *testing.T) {
	b := webBundle(t)
	for name, fn := range map[string]func(){
		"nil bundle":  func() { i18n.Middleware[*tctx](nil) },
		"zero source": func() { i18n.MiddlewareWithConfig(i18n.Config[*tctx]{Bundle: b, Sources: []i18n.Source{{}}}) },
		"FromParam":   func() { i18n.FromParam("") },
		"FromCookie":  func() { i18n.FromCookie("") },
		"FromQuery":   func() { i18n.FromQuery("") },
		"FromHeader":  func() { i18n.FromHeader("") },
		"FromFunc":    func() { i18n.FromFunc[*tctx]("", nil) },
	} {
		func() {
			defer func() {
				msg, _ := recover().(string)
				if !strings.HasPrefix(msg, "i18n: ") {
					t.Errorf("%s: panic = %q, want an i18n panic", name, msg)
				}
			}()
			fn()
		}()
	}
}
