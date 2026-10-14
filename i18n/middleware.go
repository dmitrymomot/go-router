package i18n

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"slices"

	"github.com/dmitrymomot/go-router"
)

// Source reads a locale from a request. It carries the request header it reads,
// so that [MiddlewareWithConfig] can name every such header in Vary. Build one
// with [FromParam], [FromCookie], [FromQuery], [FromHeader],
// [FromAcceptLanguage] or [FromFunc]; the zero Source is invalid.
type Source struct {
	// read reports the index of the locale it found in b.
	read func(b *Bundle, c router.Context) (int, bool)
	// ctx is the context type a FromFunc source reads, or nil for a source
	// that reads any.
	ctx  reflect.Type
	vary string
}

// FromParam reads the locale from the route parameter name, as in
// "/{lang}/posts". Declare the parameter with a class from [Bundle.Has], so a
// tag the bundle lacks never matches the route:
//
//	r.ParamClass("locale", bundle.Has)
//	r.Route("/{lang:locale}", func(g *router.Router[*Context]) { ... })
func FromParam(name string) Source {
	mustName("FromParam", name)
	return Source{read: func(b *Bundle, c router.Context) (int, bool) {
		return b.match(c.Param(name))
	}}
}

// FromCookie reads the locale from the cookie name. A missing or unknown value
// passes to the next source. The middleware never writes the cookie: storing a
// choice of the user is up to the application.
func FromCookie(name string) Source {
	mustName("FromCookie", name)
	return Source{vary: router.HeaderCookie, read: func(b *Bundle, c router.Context) (int, bool) {
		ck, err := c.Request().Cookie(name)
		if err != nil {
			return 0, false
		}
		return b.match(ck.Value)
	}}
}

// FromQuery reads the locale from the query parameter name. It reads the URL
// alone, through the query that router.Base.Query caches, and never parses a
// form body.
func FromQuery(name string) Source {
	mustName("FromQuery", name)
	return Source{read: func(b *Bundle, c router.Context) (int, bool) {
		return b.match(baseOf(c).Query(name))
	}}
}

// baseOf returns the router.Base of c. Every router.Context embeds one, so the
// lookup cannot miss.
func baseOf(c router.Context) *router.Base {
	base, _ := router.FromContext(c)
	return base
}

// FromHeader reads the locale from a request header that holds one tag, such
// as X-Locale.
func FromHeader(name string) Source {
	mustName("FromHeader", name)
	name = http.CanonicalHeaderKey(name)
	return Source{vary: name, read: func(b *Bundle, c router.Context) (int, bool) {
		return b.match(c.Request().Header.Get(name))
	}}
}

// FromAcceptLanguage negotiates the locale from the Accept-Language header,
// with its q values.
func FromAcceptLanguage() Source {
	return Source{vary: router.HeaderAcceptLanguage, read: func(b *Bundle, c router.Context) (int, bool) {
		return b.negotiate(c.Request().Header.Get(router.HeaderAcceptLanguage))
	}}
}

// FromFunc reads the locale with fn, which receives the context of the
// application, so a locale that a signed-in user saved is a field away:
//
//	i18n.FromFunc("", func(c *Context) (string, bool) {
//		if c.User == nil {
//			return "", false
//		}
//		return c.User.Locale, true
//	})
//
// fn reports false, or a tag the bundle lacks, to pass to the next source.
// varyHeader names the request header fn reads, or is "" when it reads none.
// C must be the context type of the router: [MiddlewareWithConfig] panics on a
// FromFunc of another type.
//
// FromFunc panics if fn is nil.
func FromFunc[C router.Context](varyHeader string, fn func(c C) (string, bool)) Source {
	if fn == nil {
		panic("i18n: FromFunc needs a function")
	}
	if varyHeader != "" {
		varyHeader = http.CanonicalHeaderKey(varyHeader)
	}
	return Source{vary: varyHeader, ctx: reflect.TypeFor[C](), read: func(b *Bundle, c router.Context) (int, bool) {
		tag, ok := fn(c.(C))
		if !ok {
			return 0, false
		}
		return b.match(tag)
	}}
}

func mustName(fn, name string) {
	if name == "" {
		panic("i18n: " + fn + " needs a name")
	}
}

// Config configures [MiddlewareWithConfig]. Bundle is required. Sources are
// asked in order and the first that reports a locale wins; an empty list asks
// [FromAcceptLanguage] alone. The default locale of the bundle answers when no
// source does. A request for which Skip reports true passes on with no
// [Localizer], so [From] answers with the zero one.
type Config[C router.Context] struct {
	Skip    func(c C) bool
	Bundle  *Bundle
	Sources []Source
}

// Middleware resolves the locale of each request from Accept-Language, then the
// default locale of b, and puts a [Localizer] on the request context, where
// [From] and the context functions such as [T] read it: through the handler
// context, through c.Request().Context(), and through the ctx a templ
// component renders with, as in { i18n.T(ctx, "app.title") }. It sets
// Content-Language and adds Accept-Language to Vary.
//
// Middleware panics if b is nil.
func Middleware[C router.Context](b *Bundle) router.Middleware[C] {
	return MiddlewareWithConfig(Config[C]{Bundle: b})
}

// MiddlewareWithConfig is [Middleware] with a configuration.
//
// Vary names the header of every source in the list, not the header of the
// source that won: a request without the cookie would negotiate from
// Accept-Language, so a cache keyed on the URL alone would serve the wrong
// language. router.AddVary adds them, so a name already listed, or a Vary of
// "*", is left as it is.
//
// MiddlewareWithConfig panics if Bundle is nil, a source is the zero Source,
// or a [FromFunc] source reads a context type other than C.
func MiddlewareWithConfig[C router.Context](cfg Config[C]) router.Middleware[C] {
	b := cfg.Bundle
	if b == nil {
		panic("i18n: MiddlewareWithConfig needs a Bundle")
	}
	sources := slices.Clone(cfg.Sources)
	if len(sources) == 0 {
		sources = []Source{FromAcceptLanguage()}
	}
	var vary []string
	for i, s := range sources {
		if s.read == nil {
			panic(fmt.Sprintf("i18n: MiddlewareWithConfig got the zero Source at index %d", i))
		}
		if s.ctx != nil && !reflect.TypeFor[C]().AssignableTo(s.ctx) {
			panic(fmt.Sprintf("i18n: MiddlewareWithConfig[%v] got a FromFunc for %v at index %d", reflect.TypeFor[C](), s.ctx, i))
		}
		if s.vary != "" && !slices.Contains(vary, s.vary) {
			vary = append(vary, s.vary)
		}
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if cfg.Skip != nil && cfg.Skip(c) {
				return next(c)
			}
			idx := b.defaultIdx
			for _, s := range sources {
				if i, ok := s.read(b, c); ok {
					idx = i
					break
				}
			}
			h := c.Response().Header()
			router.AddVary(h, vary...)
			// A shared slice of capacity one: a later Add copies it rather
			// than writing into another response's header.
			h[router.HeaderContentLanguage] = b.locales[idx].contentLanguage
			c.SetContext(context.WithValue(c.Request().Context(), ctxKey{}, Localizer{b: b, idx: idx}))
			return next(c)
		}
	}
}
