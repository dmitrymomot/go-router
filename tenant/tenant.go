// Package tenant resolves the tenant of a request and puts it on the request
// context.
//
// A [Source] reads a candidate id from the request: a route parameter, which
// covers both a host pattern such as "{tenant}.example.com" and a path such as
// "/t/{tenant}", the host itself for a custom domain, a header, a cookie, or a
// function of the application context. A Lookup of the application turns the
// id into its own tenant type, such as a row of the tenants table, and reports
// [ErrUnknown] for an id that names no tenant. The first source whose id the
// Lookup knows wins.
//
//	r.Host("{tenant}.example.com", func(h *router.Router[*Context]) {
//		h.Use(tenant.Middleware[*Context](store.Workspace))
//		h.GET("/", dashboard)
//	})
//
//	func dashboard(c *Context) error {
//		w, _ := tenant.From[Workspace](c)
//		...
//	}
//
// [From] reads the tenant through any context of the request: the handler
// context, c.Request().Context(), the ctx of a templ component, and a context
// derived from any of them. A background job puts a tenant on its own context
// with [NewContext].
//
// A request that names no tenant the Lookup knows is refused with
// router.ErrNotFound, which leaks nothing about which tenants exist. Set
// Config.Optional for a scope that also serves requests without a tenant.
package tenant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"

	"github.com/dmitrymomot/go-router"
)

var (
	// ErrUnknown is what a Lookup reports for an id that names no tenant. The
	// middleware then asks the next source.
	ErrUnknown = errors.New("tenant: unknown tenant")

	// ErrMissing is the cause of the 404 for a request in which no source
	// found an id at all.
	ErrMissing = errors.New("tenant: the request names no tenant")
)

// DefaultParam is the route parameter that [Middleware] reads, as in
// "{tenant}.example.com".
const DefaultParam = "tenant"

// Source reads a candidate tenant id from a request. It carries the request
// header it reads, so that the middleware can name every such header in Vary.
// Build one with [FromParam], [FromHost], [FromHeader], [FromCookie] or
// [FromFunc]; the zero Source is invalid.
//
// An id from the host, a header or a cookie is whatever the client sent. The
// Lookup decides which ids name a tenant, and membership of the user in the
// tenant stays a check of the application.
type Source struct {
	read func(c router.Context) (string, bool)
	// ctx is the context type a FromFunc source reads, or nil for a source
	// that reads any.
	ctx  reflect.Type
	vary string
}

// FromParam reads the id from the route parameter name, of the host or of the
// path: "{tenant}.example.com" and "/t/{tenant}" both carry it.
//
// FromParam panics if name is empty.
func FromParam(name string) Source {
	mustName("FromParam", name)
	return Source{read: func(c router.Context) (string, bool) {
		v := c.Param(name)
		return v, v != ""
	}}
}

// FromHost reads the id from the host of the request, lowercased and without
// its port, for a tenant on a custom domain such as "shop.acme.com".
func FromHost() Source {
	return Source{read: func(c router.Context) (string, bool) {
		v := c.Host()
		return v, v != ""
	}}
}

// FromHeader reads the id from the request header name, such as X-Tenant-ID.
// A client sets any header it likes, so read one only behind a gateway that
// sets or strips it, or between services.
//
// FromHeader panics if name is empty.
func FromHeader(name string) Source {
	mustName("FromHeader", name)
	name = http.CanonicalHeaderKey(name)
	return Source{vary: name, read: func(c router.Context) (string, bool) {
		v := c.Request().Header.Get(name)
		return v, v != ""
	}}
}

// FromCookie reads the id from the cookie name. The middleware never writes
// the cookie.
//
// FromCookie panics if name is empty.
func FromCookie(name string) Source {
	mustName("FromCookie", name)
	return Source{vary: router.HeaderCookie, read: func(c router.Context) (string, bool) {
		ck, err := c.Request().Cookie(name)
		if err != nil {
			return "", false
		}
		return ck.Value, ck.Value != ""
	}}
}

// FromFunc reads the id with fn, which receives the context of the
// application, so the tenant of an API key that an earlier middleware checked
// is a field away:
//
//	tenant.FromFunc("", func(c *Context) (string, bool) {
//		if c.APIKey == nil {
//			return "", false
//		}
//		return c.APIKey.TenantID, true
//	})
//
// fn reports false to pass to the next source. varyHeader names the request
// header fn reads, or is "" when it reads none. C must be the context type of
// the router: [MiddlewareWithConfig] panics on a FromFunc of another type.
//
// FromFunc panics if fn is nil.
func FromFunc[C router.Context](varyHeader string, fn func(c C) (string, bool)) Source {
	if fn == nil {
		panic("tenant: FromFunc needs a function")
	}
	if varyHeader != "" {
		varyHeader = http.CanonicalHeaderKey(varyHeader)
	}
	return Source{vary: varyHeader, ctx: reflect.TypeFor[C](), read: func(c router.Context) (string, bool) {
		return fn(c.(C))
	}}
}

func mustName(fn, name string) {
	if name == "" {
		panic("tenant: " + fn + " needs a name")
	}
}

// Config configures [MiddlewareWithConfig]. T is the tenant type of the
// application.
//
// Lookup is required. It turns an id into the tenant, and reports [ErrUnknown]
// for an id that names none, so the next source is asked. Any other error
// stops the request: a *router.HTTPError, such as router.ErrNotFound for a
// suspended tenant, goes to the error handler as it stands, and anything else
// as a 500. It runs with the handler context, and it must be safe for
// concurrent use.
//
// Sources are asked in order, and an empty list reads [FromParam] of
// [DefaultParam]. Put a source the application vouches for, such as the
// tenant of a checked API key, before one the client controls.
//
// A request that names no tenant the Lookup knows is refused with
// router.ErrNotFound, wrapping [ErrMissing] when no source found an id and
// [ErrUnknown] when the Lookup knew none of them. OnReject answers it: return
// an error to report it in place of the request, or nil to say that OnReject
// wrote the answer itself; either way the request goes no further. A nil
// OnReject reports err. Optional lets such a request pass with no tenant, so
// [From] reports false.
//
// A request for which Skip reports true passes with no tenant.
type Config[C router.Context, T any] struct {
	Skip     func(c C) bool
	Lookup   func(ctx context.Context, id string) (T, error)
	OnReject func(c C, err error) error
	Sources  []Source
	Optional bool
}

// Middleware resolves the tenant from the route parameter "tenant", as in
// "{tenant}.example.com", through lookup, and refuses with a 404 a request
// whose tenant lookup does not know.
//
// Middleware panics if lookup is nil.
func Middleware[C router.Context, T any](lookup func(ctx context.Context, id string) (T, error)) router.Middleware[C] {
	return MiddlewareWithConfig(Config[C, T]{Lookup: lookup})
}

// MiddlewareWithConfig is [Middleware] with a configuration.
//
// Vary names the header of every source in the list, not only the header of
// the source that won, since a request without that header would resolve
// from another source.
//
// MiddlewareWithConfig panics if Lookup is nil, a source is the zero Source,
// or a [FromFunc] source reads a context type other than C.
func MiddlewareWithConfig[C router.Context, T any](cfg Config[C, T]) router.Middleware[C] {
	if cfg.Lookup == nil {
		panic("tenant: MiddlewareWithConfig needs a Lookup")
	}
	sources := slices.Clone(cfg.Sources)
	if len(sources) == 0 {
		sources = []Source{FromParam(DefaultParam)}
	}
	var vary []string
	for i, s := range sources {
		if s.read == nil {
			panic(fmt.Sprintf("tenant: MiddlewareWithConfig got the zero Source at index %d", i))
		}
		if s.ctx != nil && !reflect.TypeFor[C]().AssignableTo(s.ctx) {
			panic(fmt.Sprintf("tenant: MiddlewareWithConfig[%v] got a FromFunc for %v at index %d", reflect.TypeFor[C](), s.ctx, i))
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
			router.AddVary(c.Response().Header(), vary...)

			cause := ErrMissing
			for _, s := range sources {
				id, ok := s.read(c)
				if !ok {
					continue
				}
				t, err := cfg.Lookup(c, id)
				switch {
				case err == nil:
					c.SetContext(NewContext(c.Request().Context(), t))
					return next(c)
				case errors.Is(err, ErrUnknown):
					cause = ErrUnknown
					continue
				}
				if _, ok := errors.AsType[*router.HTTPError](err); ok {
					return err
				}
				return router.ErrInternalServerError.WithError(fmt.Errorf("tenant: look up %q: %w", id, err))
			}

			if cfg.Optional {
				return next(c)
			}
			err := router.ErrNotFound.WithError(cause)
			if cfg.OnReject == nil {
				return err
			}
			return cfg.OnReject(c, err)
		}
	}
}

// ctxKey carries the tenant of type T. Each tenant type has a key of its own.
type ctxKey[T any] struct{}

// NewContext returns a copy of ctx that carries t. The middleware calls it on
// each request; a job that runs outside a request calls it itself.
func NewContext[T any](ctx context.Context, t T) context.Context {
	return context.WithValue(ctx, ctxKey[T]{}, t)
}

// From reports the tenant of type T that ctx carries. ok is false when ctx
// carries none, as for a request of an Optional scope that named no tenant.
func From[T any](ctx context.Context) (T, bool) {
	t, ok := ctx.Value(ctxKey[T]{}).(T)
	return t, ok
}
