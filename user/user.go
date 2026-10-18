// Package user resolves the user of a request and puts it on the request
// context.
//
// [New] takes the one function that knows who the user is: it reads the
// session, an API key or anything else on the context of the application, and
// returns the user type of the application, such as a row of the accounts
// table. It reports [ErrUnknown] for a visitor.
//
//	users := user.New(func(c *Context) (Account, error) {
//		s, ok := sessions.From(c)
//		if !ok {
//			return Account{}, user.ErrUnknown
//		}
//		return c.Store.Account(c, s.UserID)
//	})
//
//	r.Use(sessions.Middleware(), users.Middleware())
//	r.GET("/", home)
//	r.With(users.Require(user.RequireConfig{LoginURL: "/login"})).GET("/settings", settings)
//
//	func settings(c *Context) error {
//		a, _ := users.From(c)
//		...
//	}
//
// The middleware only resolves: a visitor passes, and From reports false.
// Require refuses a visitor: it answers 401, or sends a browser to the login
// page with the path it asked for. One [Users] value binds the context type
// and the user type once, so the middleware, Require and From cannot disagree
// on either.
//
// From reads the user through any context of the request: the handler
// context, c.Request().Context(), the ctx of a templ component, and a context
// derived from any of them. The package function [From] does the same where
// the Users value is out of reach. A background job puts a user on its own
// context with [NewContext].
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

	"github.com/dmitrymomot/go-router"
)

var (
	// ErrUnknown is what the load function of [New] reports for a visitor.
	// The middleware then lets the request pass with no user.
	ErrUnknown = errors.New("user: unknown user")

	// ErrMissing is the cause of the 401 of [Users.Require] for a request
	// that carries no user.
	ErrMissing = errors.New("user: the request names no user")
)

const (
	// DefaultNextParam is the query parameter of the login URL that carries
	// the path a browser asked for, as in "/login?next=%2Fsettings".
	DefaultNextParam = "next"

	// DefaultChallenge is the WWW-Authenticate value of the 401 of
	// [Users.Require].
	DefaultChallenge = "Bearer"
)

// Users resolves the user of type T for a router whose context type is C.
// Build one with [New]; the zero Users is invalid. A Users is safe for
// concurrent use.
type Users[C router.Context, T any] struct {
	load func(c C) (T, error)
}

// New builds a [Users] that resolves the user with load. load reports
// [ErrUnknown] for a visitor. Any other error stops the request: a
// *router.HTTPError, such as router.ErrForbidden for a suspended account, goes
// to the error handler as it stands, and anything else as a 500. load runs
// once per request, and it must be safe for concurrent use.
//
// New panics if load is nil.
func New[C router.Context, T any](load func(c C) (T, error)) *Users[C, T] {
	if load == nil {
		panic("user: New needs a load function")
	}
	return &Users[C, T]{load: load}
}

// Middleware resolves the user of each request and puts it on the request
// context, where [Users.From] reads it. A visitor passes with no user.
func (u *Users[C, T]) Middleware() router.Middleware[C] {
	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			v, err := u.load(c)
			switch {
			case err == nil:
				c.SetContext(NewContext(c.Request().Context(), v))
				return next(c)
			case errors.Is(err, ErrUnknown):
				return next(c)
			}
			if _, ok := errors.AsType[*router.HTTPError](err); ok {
				return err
			}
			return router.ErrInternalServerError.WithError(fmt.Errorf("user: load the user: %w", err))
		}
	}
}

// RequireConfig configures [Users.Require].
//
// LoginURL is the login page of a browser, a local path such as "/login".
// When it is set, a request with no user from a client that prefers HTML is
// sent there with a 303, and a GET or HEAD request adds the path and query the
// client asked for, with the prefix of a mount, as the NextParam parameter, so
// the login handler sends the user back with c.LocalRedirect. Any other method
// gets no next value, since the login could not repeat the request. A client
// that prefers JSON, and every client when LoginURL is empty, gets
// router.ErrUnauthorized.
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

// Require refuses a request with no user, as [RequireConfig] describes. It goes
// inside [Users.Middleware].
//
// Require panics if LoginURL is set and is not a local path, as
// [router.IsLocalURL] decides, or holds a fragment.
func (u *Users[C, T]) Require(cfg RequireConfig) router.Middleware[C] {
	var login *url.URL
	if cfg.LoginURL != "" {
		if !router.IsLocalURL(cfg.LoginURL) {
			panic(fmt.Sprintf("user: Require needs a LoginURL that is a local path, and got %q", cfg.LoginURL))
		}
		l, err := url.Parse(cfg.LoginURL)
		if err != nil || l.Fragment != "" {
			panic(fmt.Sprintf("user: Require cannot use the LoginURL %q", cfg.LoginURL))
		}
		login = l
	}
	param := cmp.Or(cfg.NextParam, DefaultNextParam)
	challenge := cmp.Or(cfg.Challenge, DefaultChallenge)

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if _, ok := u.From(c); ok {
				return next(c)
			}
			if login == nil || prefersJSON(c) {
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

// From reports the user that ctx carries. ok is false for a visitor.
func (u *Users[C, T]) From(ctx context.Context) (T, bool) {
	return From[T](ctx)
}

// prefersJSON reports whether the client ranks JSON above HTML, as an API
// client does. A browser, and a client that sends no Accept, prefer HTML.
func prefersJSON(c router.Context) bool {
	b, ok := router.FromContext(c)
	return ok && b.Accepts(router.MIMETextHTML, router.MIMEApplicationJSON) == router.MIMEApplicationJSON
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
