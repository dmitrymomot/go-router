// Package user resolves the user of a request and puts it on the request
// context.
//
// A [Source] reads a candidate id from the request: a signed cookie, a Bearer
// token, a header, or a function of the application context. A Lookup of the
// application turns the id into its own user type, such as a row of the
// accounts table, and reports [ErrUnknown] for an id that names no user. The
// first source whose id the Lookup knows wins.
//
//	r.Use(session.Middleware[*Context, Data](sessions))
//	r.Use(user.Middleware[*Context](store.Account, user.FromSession(func(d Data) string { return d.UserID })))
//	r.GET("/settings", settings)
//
//	func settings(c *Context) error {
//		a, _ := user.From[Account](c)
//		...
//	}
//
// [From] reads the user through any context of the request: the handler
// context, c.Request().Context(), the ctx of a templ component, and a context
// derived from any of them. A background job puts a user on its own context
// with [NewContext].
//
// The middleware only resolves: a request that names no user the Lookup
// knows passes as a visitor, and [From] reports false. Put [Require] on the
// routes that need a user: it answers 401, or sends a browser to the login
// page with the path it asked for.
//
// The package checks who the user is, and nothing else. Membership of the
// user in a tenant, and what the user may do there, stay checks of the
// application.
package user

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/cookie"
	"github.com/dmitrymomot/go-router/session"
)

var (
	// ErrUnknown is what a Lookup reports for an id that names no user. The
	// middleware then asks the next source.
	ErrUnknown = errors.New("user: unknown user")

	// ErrMissing is the cause of the 401 of [Require] for a request that
	// carries no user.
	ErrMissing = errors.New("user: the request names no user")
)

// DefaultNextParam is the query parameter of the login URL that carries the
// path a browser asked for, as in "/login?next=%2Fsettings".
const DefaultNextParam = "next"

// DefaultChallenge is the WWW-Authenticate value of the 401 of [Require].
const DefaultChallenge = "Bearer"

// Source reads a candidate user id from a request. It carries the request
// header it reads, so that the middleware can name every such header in Vary.
// Build one with [FromSession], [FromCookie], [FromBearer], [FromHeader] or
// [FromFunc]; the zero Source is invalid.
//
// An id from a plain header is whatever the client sent. The signed cookie and
// a session are safe to trust; a Bearer token is safe when the Lookup checks
// it, as a lookup of the token in the database does.
type Source struct {
	read func(c router.Context) (string, bool)
	// ctx is the context type a FromFunc source reads, or nil for a source
	// that reads any.
	ctx  reflect.Type
	vary string
}

// FromSession reads the id from the session value of type T that
// session.Middleware loaded, through id, so the session middleware goes
// first:
//
//	r.Use(session.Middleware[*Context, Data](store))
//	r.Use(user.Middleware[*Context](accounts.Find, user.FromSession(func(d Data) string { return d.UserID })))
//
// A request with no session, or a session whose id reports "", passes to the
// next source.
//
// FromSession panics if id is nil.
func FromSession[T any](id func(s T) string) Source {
	if id == nil {
		panic("user: FromSession needs a function")
	}
	return Source{vary: router.HeaderCookie, read: func(c router.Context) (string, bool) {
		s, ok := session.From[T](c)
		if !ok {
			return "", false
		}
		v := id(s)
		return v, v != ""
	}}
}

// FromCookie reads the id from the cookie name, which codec verifies, so a
// client cannot forge the id of another user. The application writes the
// cookie with codec.Set at login and drops it with c.ClearCookie at logout;
// the middleware never writes it. A cookie that does not verify, or that ran
// out, passes to the next source.
//
// FromCookie panics if codec is nil or name is empty.
func FromCookie(codec *cookie.Codec, name string) Source {
	if codec == nil {
		panic("user: FromCookie needs a codec")
	}
	mustName("FromCookie", name)
	return Source{vary: router.HeaderCookie, read: func(c router.Context) (string, bool) {
		v, err := codec.Get(c, name)
		if err != nil {
			return "", false
		}
		return v, v != ""
	}}
}

// FromBearer reads the token of an "Authorization: Bearer <token>" header.
// The scheme is matched without regard to case. The Lookup receives the token
// as the id, so it checks the token, by a hash in the database for one.
func FromBearer() Source {
	return Source{vary: router.HeaderAuthorization, read: func(c router.Context) (string, bool) {
		scheme, token, ok := strings.Cut(c.Request().Header.Get(router.HeaderAuthorization), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			return "", false
		}
		token = strings.TrimSpace(token)
		return token, token != ""
	}}
}

// FromHeader reads the id from the request header name, such as X-User-ID. A
// client sets any header it likes, so read one only behind a gateway that
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

// FromFunc reads the id with fn, which receives the context of the
// application, so the user of an API key that an earlier middleware checked is
// a field away:
//
//	user.FromFunc("", func(c *Context) (string, bool) {
//		if c.APIKey == nil {
//			return "", false
//		}
//		return c.APIKey.UserID, true
//	})
//
// fn reports false to pass to the next source. varyHeader names the request
// header fn reads, or is "" when it reads none. C must be the context type of
// the router: [MiddlewareWithConfig] panics on a FromFunc of another type.
//
// FromFunc panics if fn is nil.
func FromFunc[C router.Context](varyHeader string, fn func(c C) (string, bool)) Source {
	if fn == nil {
		panic("user: FromFunc needs a function")
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
		panic("user: " + fn + " needs a name")
	}
}

// Config configures [MiddlewareWithConfig]. T is the user type of the
// application.
//
// Lookup is required. It turns an id into the user, and reports [ErrUnknown]
// for an id that names none, so the next source is asked. Any other error
// stops the request: a *router.HTTPError, such as router.ErrForbidden for a
// suspended account, goes to the error handler as it stands, and anything
// else as a 500. It runs with the handler context, and it must be safe for
// concurrent use.
//
// Sources are asked in order, and at least one is required. Put a source the
// application vouches for, such as the session, before one the client
// controls.
//
// A request that names no user the Lookup knows passes with no user, so
// [From] reports false. So does a request for which Skip reports true.
type Config[C router.Context, T any] struct {
	Skip    func(c C) bool
	Lookup  func(ctx context.Context, id string) (T, error)
	Sources []Source
}

// Middleware resolves the user from sources, asked in order, through lookup,
// and lets a request with no user pass, so [From] reports false. Put
// [Require] on the routes that need a user.
//
// Middleware panics if lookup is nil or sources is empty.
func Middleware[C router.Context, T any](lookup func(ctx context.Context, id string) (T, error), sources ...Source) router.Middleware[C] {
	return MiddlewareWithConfig(Config[C, T]{Lookup: lookup, Sources: sources})
}

// MiddlewareWithConfig is [Middleware] with a configuration.
//
// Vary names the header of every source in the list, not only the header of
// the source that won, since a request without that header would resolve
// from another source.
//
// MiddlewareWithConfig panics if Lookup is nil, Sources is empty, a source is
// the zero Source, or a [FromFunc] source reads a context type other than C.
func MiddlewareWithConfig[C router.Context, T any](cfg Config[C, T]) router.Middleware[C] {
	if cfg.Lookup == nil {
		panic("user: MiddlewareWithConfig needs a Lookup")
	}
	if len(cfg.Sources) == 0 {
		panic("user: MiddlewareWithConfig needs at least one Source")
	}
	sources := slices.Clone(cfg.Sources)
	var vary []string
	for i, s := range sources {
		if s.read == nil {
			panic(fmt.Sprintf("user: MiddlewareWithConfig got the zero Source at index %d", i))
		}
		if s.ctx != nil && !reflect.TypeFor[C]().AssignableTo(s.ctx) {
			panic(fmt.Sprintf("user: MiddlewareWithConfig[%v] got a FromFunc for %v at index %d", reflect.TypeFor[C](), s.ctx, i))
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

			for _, s := range sources {
				id, ok := s.read(c)
				if !ok {
					continue
				}
				u, err := cfg.Lookup(c, id)
				switch {
				case err == nil:
					c.SetContext(NewContext(c.Request().Context(), u))
					return next(c)
				case errors.Is(err, ErrUnknown):
					continue
				}
				if _, ok := errors.AsType[*router.HTTPError](err); ok {
					return err
				}
				// The id may be a secret, such as a Bearer token, so it stays
				// out of the error.
				return router.ErrInternalServerError.WithError(fmt.Errorf("user: look up the user: %w", err))
			}

			return next(c)
		}
	}
}

// RequireConfig configures [RequireWithConfig].
//
// LoginURL is the login page of a browser, a local path such as "/login".
// When it is set, a request with no user is sent there with a 303, and a GET
// or HEAD request adds the path and query the client asked for, with the
// prefix of a mount, as the NextParam parameter, so the login handler sends
// the user back with c.LocalRedirect. Any other
// method gets no next value, since the login could not repeat the request.
// When LoginURL is empty, the request is refused with router.ErrUnauthorized,
// which suits an API.
//
// NextParam is the query parameter of LoginURL that carries the path, and
// defaults to [DefaultNextParam].
//
// Challenge is the WWW-Authenticate value of the 401, and an empty one sends
// [DefaultChallenge], as middleware.KeyAuth does. A header that an earlier
// middleware set stays.
type RequireConfig struct {
	LoginURL  string
	NextParam string
	Challenge string
}

// Require refuses with router.ErrUnauthorized, and a WWW-Authenticate of
// [DefaultChallenge], a request that carries no user of type T, which suits an
// API. It goes inside [Middleware] or
// [MiddlewareWithConfig], which put the user on the context.
func Require[C router.Context, T any]() router.Middleware[C] {
	return RequireWithConfig[C, T](RequireConfig{})
}

// RequireWithConfig is [Require] with a configuration, which sends a browser
// to the login page.
//
// RequireWithConfig panics if LoginURL is set and is not a local path, as
// [router.IsLocalURL] decides, or holds a fragment.
func RequireWithConfig[C router.Context, T any](cfg RequireConfig) router.Middleware[C] {
	var login *url.URL
	if cfg.LoginURL != "" {
		if !router.IsLocalURL(cfg.LoginURL) {
			panic(fmt.Sprintf("user: RequireWithConfig needs a LoginURL that is a local path, and got %q", cfg.LoginURL))
		}
		u, err := url.Parse(cfg.LoginURL)
		if err != nil || u.Fragment != "" {
			panic(fmt.Sprintf("user: RequireWithConfig cannot use the LoginURL %q", cfg.LoginURL))
		}
		login = u
	}
	param := cmp.Or(cfg.NextParam, DefaultNextParam)
	challenge := cmp.Or(cfg.Challenge, DefaultChallenge)

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if _, ok := From[T](c); ok {
				return next(c)
			}
			if login == nil {
				if h := c.Response().Header(); h.Get(router.HeaderWWWAuthenticate) == "" {
					h.Set(router.HeaderWWWAuthenticate, challenge)
				}
				return router.ErrUnauthorized.WithError(ErrMissing)
			}
			target := *login
			if m := c.Request().Method; m == http.MethodGet || m == http.MethodHead {
				q := target.Query()
				q.Set(param, nextOf(c.Request()))
				target.RawQuery = q.Encode()
			}
			c.Response().Header().Set(router.HeaderLocation, target.String())
			c.Response().WriteHeader(http.StatusSeeOther)
			return nil
		}
	}
}

// nextOf reports the path and query the client asked for. RequestURI keeps
// it whole where MountHandler strips a prefix from URL.Path; a request built
// by hand, or one in absolute form for a proxy, falls back to URL.
func nextOf(r *http.Request) string {
	if router.IsLocalURL(r.RequestURI) {
		return r.RequestURI
	}
	return r.URL.RequestURI()
}

// ctxKey carries the user of type T. Each user type has a key of its own.
type ctxKey[T any] struct{}

// NewContext returns a copy of ctx that carries u. The middleware calls it on
// each request; a job that runs outside a request, on behalf of a user, calls
// it itself.
func NewContext[T any](ctx context.Context, u T) context.Context {
	return context.WithValue(ctx, ctxKey[T]{}, u)
}

// From reports the user of type T that ctx carries. ok is false when ctx
// carries none, as for a visitor.
func From[T any](ctx context.Context) (T, bool) {
	u, ok := ctx.Value(ctxKey[T]{}).(T)
	return u, ok
}
