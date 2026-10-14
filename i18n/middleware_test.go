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

type otherCtx struct{ router.Base }

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

// TestMiddlewareReachesEveryContext renders a component the way templ does,
// with whatever ctx it is handed, through each context a request offers.
func TestMiddlewareReachesEveryContext(t *testing.T) {
	items := router.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		_, err := io.WriteString(w, i18n.TN(ctx, "app.items", 3)+" "+i18n.T(ctx, "app.hi"))
		return err
	})
	r := newRouter()
	r.Use(i18n.Middleware[*tctx](webBundle(t)))
	r.GET("/render", func(c *tctx) error { return c.Render(http.StatusOK, items) })
	r.GET("/stream", func(c *tctx) error { return c.RenderStream(http.StatusOK, items) })
	r.GET("/request", func(c *tctx) error {
		// A service handed the request context, as SetContext advises.
		var buf strings.Builder
		if err := items.Render(c.Request().Context(), &buf); err != nil {
			return err
		}
		return c.String(http.StatusOK, buf.String())
	})
	r.GET("/derived", func(c *tctx) error {
		ctx, cancel := context.WithCancel(c.Request().Context())
		defer cancel()
		var buf strings.Builder
		if err := items.Render(ctx, &buf); err != nil {
			return err
		}
		return c.String(http.StatusOK, buf.String())
	})
	// A plain http.Handler, as templ.Handler is, sees only r.Context().
	r.GET("/handler", router.WrapHandler[*tctx](http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = items.Render(req.Context(), w)
	})))

	// uk has no app.items, so that key falls back to en.
	const want = "3 items Привіт"
	for _, path := range []string{"/render", "/stream", "/request", "/derived", "/handler"} {
		rec := serve(r, path, map[string]string{"Accept-Language": "uk"})
		if got := rec.Body.String(); got != want {
			t.Errorf("GET %s = %q, want %q", path, got, want)
		}
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
		"FromFunc of another context": func() {
			i18n.MiddlewareWithConfig(i18n.Config[*tctx]{Bundle: b, Sources: []i18n.Source{
				i18n.FromFunc("", func(*otherCtx) (string, bool) { return "", false }),
			}})
		},
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
