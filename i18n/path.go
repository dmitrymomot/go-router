package i18n

import (
	"net/http"

	"github.com/dmitrymomot/go-router"
)

// Has reports whether tag names a locale of the bundle exactly as written, in
// the canonical form that [Locale.Tag] reports: "en-GB", not "en-gb" or
// "en_GB". It suits router.Router.ParamClass, so a path holds each locale under
// one spelling and a tag the catalogs lack is a 404:
//
//	r.ParamClass("locale", bundle.Has)
//	r.Route("/{lang:locale}", func(g *router.Router[*Context]) { ... })
func (b *Bundle) Has(tag string) bool {
	_, ok := b.byTag[tag]
	return ok
}

// RedirectToLocale answers with a redirect to pattern, whose parameter name
// holds the locale of the request, as in a handler for "/" that sends the
// visitor to "/{lang}". The locale is the one the middleware resolved, or,
// on a route the middleware does not cover, the one Accept-Language negotiates
// with b; the redirect is a 302 that varies by Accept-Language, since another
// visitor gets another locale.
//
//	r.GET("/", i18n.RedirectToLocale[*Context](bundle, "/{lang}", "lang"))
//
// RedirectToLocale panics if b is nil or if pattern does not expand with name
// set to the default locale, as router.Expand reports.
func RedirectToLocale[C router.Context](b *Bundle, pattern, name string) router.HandlerFunc[C] {
	if b == nil {
		panic("i18n: RedirectToLocale needs a Bundle")
	}
	router.MustExpand(pattern, name, b.Default().Tag())

	return func(c C) error {
		loc, ok := lookup(c)
		if !ok {
			loc = b.For(b.Negotiate(c.Request().Header.Get("Accept-Language")))
		}
		target, err := router.Expand(pattern, name, loc.Locale().Tag())
		if err != nil {
			return err
		}
		c.Response().Header().Add("Vary", "Accept-Language")
		return baseOf(c).Redirect(http.StatusFound, target)
	}
}
