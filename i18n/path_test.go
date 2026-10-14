package i18n_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/i18n"
)

func TestHas(t *testing.T) {
	b := webBundle(t)
	for tag, want := range map[string]bool{
		"en": true, "uk": true, "en-GB": true,
		"en-gb": false, "en_GB": false, "EN": false, "fr": false, "": false,
	} {
		if got := b.Has(tag); got != want {
			t.Errorf("Has(%q) = %v, want %v", tag, got, want)
		}
	}
}

func TestLocaleInPath(t *testing.T) {
	b := webBundle(t)
	r := newRouter()
	r.ParamClass("locale", b.Has)
	r.Use(i18n.MiddlewareWithConfig(i18n.Config[*tctx]{
		Bundle:  b,
		Sources: []i18n.Source{i18n.FromParam("lang"), i18n.FromAcceptLanguage()},
	}))
	r.GET("/", i18n.RedirectToLocale[*tctx](b, "/{lang}", "lang"))
	r.Route("/{lang:locale}", func(g *router.Router[*tctx]) {
		g.GET("/", hiHandler)
	})

	rec := serve(r, "/", map[string]string{"Accept-Language": "uk"})
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/uk" {
		t.Errorf("GET / = %d %q, want 302 /uk", rec.Code, rec.Header().Get("Location"))
	}
	if got := rec.Header().Values("Vary"); !slices.Contains(got, "Accept-Language") {
		t.Errorf("Vary = %q, want Accept-Language", got)
	}

	if rec := serve(r, "/uk", nil); rec.Body.String() != "Привіт" {
		t.Errorf("GET /uk = %q", rec.Body.String())
	}
	if rec := serve(r, "/en-GB", map[string]string{"Accept-Language": "uk"}); rec.Body.String() != "Hiya" {
		t.Errorf("GET /en-GB = %q", rec.Body.String())
	}
	for _, path := range []string{"/fr", "/en-gb"} {
		if rec := serve(r, path, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

func TestRedirectToLocaleWithoutMiddleware(t *testing.T) {
	b := webBundle(t)
	r := newRouter()
	r.GET("/", i18n.RedirectToLocale[*tctx](b, "/{lang}/home", "lang"))

	for accept, want := range map[string]string{"en-GB": "/en-GB/home", "fr": "/en/home"} {
		rec := serve(r, "/", map[string]string{"Accept-Language": accept})
		if got := rec.Header().Get("Location"); got != want {
			t.Errorf("Accept-Language %q: Location = %q, want %q", accept, got, want)
		}
	}
}

func TestRedirectToLocaleRefusesPattern(t *testing.T) {
	b := webBundle(t)
	r := newRouter()
	r.GET("/", i18n.RedirectToLocale[*tctx](b, "/{lang:[a-z]+}", "lang"))

	// en-GB fails the regular expression of the pattern, so Expand refuses it.
	rec := serve(r, "/", map[string]string{"Accept-Language": "en-GB"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500", rec.Code)
	}

	for name, fn := range map[string]func(){
		"nil bundle":  func() { i18n.RedirectToLocale[*tctx](nil, "/{lang}", "lang") },
		"wrong name":  func() { i18n.RedirectToLocale[*tctx](b, "/{lang}", "locale") },
		"bad pattern": func() { i18n.RedirectToLocale[*tctx](b, "lang/{lang}", "lang") },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			fn()
		}()
	}
}
