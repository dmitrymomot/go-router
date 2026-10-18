// Package session keeps the state of a visitor across requests: a token in a
// cookie names a record in a [Store], and the record holds a value of a type
// of the application.
//
//	type Data struct {
//		UserID string
//	}
//
//	r.Use(session.Middleware[*Context, Data](store))
//
//	func login(c *Context) error {
//		... check the password ...
//		if err := session.Renew(c); err != nil {
//			return err
//		}
//		if err := session.Put(c, Data{UserID: a.ID}); err != nil {
//			return err
//		}
//		return c.Redirect(http.StatusSeeOther, "/")
//	}
//
// [From] reads the value through any context of the request. [Put] stores a
// new value, and starts a session for a visitor who has none. [Renew] gives
// the session a new token, which a login needs, so a token that an attacker
// planted before it is worth nothing after it. [Destroy] ends the session at
// logout.
//
// Put, Renew and Destroy write the store at the call and set the cookie in
// the header at once, so a failure reaches the handler that caused it, and the
// cookie is in place before the first byte of the body. Once the header went
// out they report [ErrCommitted] and change nothing.
//
// The package ships no store on the server: implement the three methods of
// [Store] on the database of the application. Such a store can revoke a
// session: the middleware loads the record on every request, so a record that
// is gone ends the session at the next one. To sign a user out on every
// device, delete each record of the user in the store. [CookieStore] keeps the
// record in the cookie and cannot revoke it.
//
// The package keeps the value and nothing else: no passwords, no users. Put
// the id of the user in the value, and let package user resolve it with
// user.FromSession.
package session

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/cookie"
)

// ErrCommitted is what [Put], [Renew] and [Destroy] report once the header of
// the response went out, since the cookie can no longer change.
var ErrCommitted = errors.New("session: the response is already committed")

const (
	// DefaultCookie is the name of the session cookie over HTTP, and
	// DefaultHostCookie its name over HTTPS. The __Host- prefix makes the
	// browser refuse the cookie unless it is Secure, at Path "/" and with no
	// Domain, so neither a parent domain nor a sibling subdomain can set a
	// cookie of that name to stand in front of it.
	DefaultCookie     = "session"
	DefaultHostCookie = "__Host-session"

	// DefaultLifetime is how long a session lasts from its start or its
	// last [Renew], whatever the visitor does.
	DefaultLifetime = 24 * time.Hour
)

// Config configures [MiddlewareWithConfig]. T is the type of the value that
// a session holds, any type that encoding/json/v2 encodes.
//
// Store is required. Cookie names the cookie. When it is empty, the cookie is
// [DefaultHostCookie] over HTTPS and [DefaultCookie] over HTTP, where a
// browser drops a Secure cookie, as router.Base.Scheme decides. The middleware
// builds the cookie as router.Base.NewCookie does: Path "/", no Domain,
// HttpOnly, SameSite=Lax, and Secure on https, with a MaxAge that ends with
// the session. A Cookie of your own with the __Host- prefix works over HTTPS
// alone.
//
// The middleware reads the first cookie of the name that the request carries
// and no other. Under the __Host- prefix that cookie is the one this host set.
// Over HTTP, and under a name of your own without the prefix, a parent domain
// can set a cookie of the same name that the browser may send first, which
// signs the visitor out or into a session of the attacker; serve HTTPS, or
// keep the parent domain to hosts you trust.
//
// Lifetime caps a session from its start or its last [Renew], and defaults
// to [DefaultLifetime]. IdleTimeout, when set, also ends a session that sees
// no request for that long: each request moves the end forward, up to the
// Lifetime, and writes the store at most once per half of IdleTimeout.
//
// A request for which Skip reports true passes with no session.
type Config[C router.Context, T any] struct {
	Skip        func(c C) bool
	Store       Store
	Cookie      string
	Lifetime    time.Duration
	IdleTimeout time.Duration
}

// Middleware loads the session of the request from store, so [From] reads
// it, and handlers can [Put], [Renew] and [Destroy] it.
//
// Middleware panics if store is nil.
func Middleware[C router.Context, T any](store Store) router.Middleware[C] {
	return MiddlewareWithConfig(Config[C, T]{Store: store})
}

// MiddlewareWithConfig is [Middleware] with a configuration.
//
// A cookie that names no live session is cleared before the handler runs, and
// so is one whose record does not decode into T. Every response of the scope
// names Cookie in Vary. An error of the store answers the request with a 500.
//
// MiddlewareWithConfig panics if Store is nil, the cookie name is not a valid
// token, or Lifetime or IdleTimeout is negative.
func MiddlewareWithConfig[C router.Context, T any](cfg Config[C, T]) router.Middleware[C] {
	if cfg.Store == nil {
		panic("session: MiddlewareWithConfig needs a Store")
	}
	if cfg.Lifetime < 0 || cfg.IdleTimeout < 0 {
		panic("session: MiddlewareWithConfig needs a Lifetime and an IdleTimeout of zero or more")
	}
	m := &manager{
		store:    cfg.Store,
		cookie:   cfg.Cookie,
		lifetime: cmp.Or(cfg.Lifetime, DefaultLifetime),
		idle:     cfg.IdleTimeout,
		typ:      reflect.TypeFor[T](),
	}
	//nolint:gosec // A cookie with only a name, to check that name.
	if m.cookie != "" && (&http.Cookie{Name: m.cookie}).Valid() != nil {
		panic(fmt.Sprintf("session: MiddlewareWithConfig cannot name a cookie %q", m.cookie))
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if cfg.Skip != nil && cfg.Skip(c) {
				return next(c)
			}
			router.AddVary(c.Response().Header(), router.HeaderCookie)

			// The context of a router always carries its Base.
			b, _ := router.FromContext(c)
			// The state goes on the context before the load, so a store
			// reads the cookie name from it.
			s := &state{m: m, b: b, name: m.cookieName(b)}
			c.SetContext(context.WithValue(c.Request().Context(), stateKey{}, s))
			if err := load[T](c, s); err != nil {
				return err
			}
			return next(c)
		}
	}
}

// manager holds the configuration of one middleware.
type manager struct {
	store Store
	typ   reflect.Type
	// cookie is the Cookie of the Config, or "" for the default of the
	// scheme.
	cookie   string
	lifetime time.Duration
	idle     time.Duration
}

// cookieName reports the name of the session cookie for the request of b.
func (m *manager) cookieName(b *router.Base) string {
	switch {
	case m.cookie != "":
		return m.cookie
	case b.Scheme() == "https":
		return DefaultHostCookie
	default:
		return DefaultCookie
	}
}

// state is the session of one request.
type state struct {
	m *manager
	b *router.Base
	// name is the name of the session cookie of this request.
	name string
	// data is the value, a T, or nil when the request has no session.
	data any
	// token is "" when the request has no session.
	token   string
	created time.Time
	expires time.Time
	// sent is true once the request carried the cookie or a Set-Cookie for it
	// went out, so Destroy knows to clear it.
	sent bool
}

type stateKey struct{}

// CookieName reports the name of the session cookie of the request that ctx
// belongs to, which a [Store] that keeps the record in the cookie binds the
// record to: the Cookie of the Config, or the default for the scheme of the
// request. Outside the middleware it reports [DefaultCookie].
func CookieName(ctx context.Context) string {
	if s, ok := ctx.Value(stateKey{}).(*state); ok {
		return s.name
	}
	return DefaultCookie
}

// record is the form a session takes in the store. The times are Unix
// seconds, which is all a cookie can carry.
type record[T any] struct {
	Data    T     `json:"d"`
	Created int64 `json:"c"`
	Expires int64 `json:"e"`
}

// load reads the session that the cookie of the request names, and moves its
// end forward under an IdleTimeout.
func load[T any](ctx context.Context, s *state) error {
	cookies := s.b.Request().CookiesNamed(s.name)
	if len(cookies) == 0 || cookies[0].Value == "" {
		return nil
	}
	token := cookies[0].Value
	s.sent = true

	raw, err := s.m.store.Load(ctx, token)
	switch {
	case errors.Is(err, ErrNotFound):
		s.clearCookie()
		return nil
	case err != nil:
		return router.ErrInternalServerError.WithError(fmt.Errorf("session: load: %w", err))
	}
	var rec record[T]
	now := time.Now()
	if ok := json.Unmarshal(raw, &rec) == nil && now.Before(time.Unix(rec.Expires, 0)); !ok {
		s.clearCookie()
		return nil
	}
	s.data, s.token = rec.Data, token
	s.created, s.expires = time.Unix(rec.Created, 0), time.Unix(rec.Expires, 0)

	if s.m.idle > 0 {
		end := s.m.expiry(s.created, now)
		if end.Sub(s.expires) >= s.m.idle/2 {
			return s.save(ctx, s.token, s.data, s.created, end)
		}
	}
	return nil
}

// expiry reports when a session that began at created ends, when the last
// request came at now.
func (m *manager) expiry(created, now time.Time) time.Time {
	end := created.Add(m.lifetime)
	if idle := now.Add(m.idle); m.idle > 0 && idle.Before(end) {
		end = idle
	}
	return end.Truncate(time.Second)
}

// save stores the record and points the cookie at it.
func (s *state) save(ctx context.Context, token string, data any, created, expires time.Time) error {
	raw, err := json.Marshal(record[any]{Data: data, Created: created.Unix(), Expires: expires.Unix()})
	if err != nil {
		return fmt.Errorf("session: encode the value: %w", err)
	}
	token, err = s.m.store.Save(ctx, token, raw, expires)
	if err != nil {
		return fmt.Errorf("session: save: %w", err)
	}
	if token != s.token || !expires.Equal(s.expires) {
		maxAge := time.Until(expires)
		if err := s.setCookie(token, max(maxAge, time.Second)); err != nil {
			return err
		}
	}
	s.data, s.token, s.created, s.expires = data, token, created, expires
	return nil
}

// setCookie writes the session cookie, in place of one that an earlier call
// of this request wrote.
func (s *state) setCookie(value string, maxAge time.Duration) error {
	line := s.b.NewCookie(s.name, value, maxAge).String()
	if len(line) > cookie.MaxSize {
		return cookie.ErrTooLarge
	}
	h := s.b.Response().Header()
	prefix := s.name + "="
	lines := h.Values(router.HeaderSetCookie)
	kept := lines[:0:0]
	for _, l := range lines {
		if !strings.HasPrefix(l, prefix) {
			kept = append(kept, l)
		}
	}
	h[router.HeaderSetCookie] = append(kept, line)
	s.sent = true
	return nil
}

func (s *state) clearCookie() {
	// A cookie with a maxAge below zero is short, so it always fits.
	_ = s.setCookie("", -1)
}

// stateOf reports the session of ctx, and panics when no middleware put one
// there: that is a route without the middleware, which no request can fix.
func stateOf(ctx context.Context, fn string) *state {
	s, ok := ctx.Value(stateKey{}).(*state)
	if !ok {
		panic("session: " + fn + " needs session.Middleware on the route")
	}
	return s
}

func (s *state) mustBeOpen() error {
	if s.b.Response().Committed {
		return ErrCommitted
	}
	return nil
}

// From reports the value of the session of ctx. ok is false when the request
// has no session, when T is not the type of the middleware, and when ctx
// passed no session middleware.
func From[T any](ctx context.Context) (T, bool) {
	s, ok := ctx.Value(stateKey{}).(*state)
	if !ok {
		var zero T
		return zero, false
	}
	v, ok := s.data.(T)
	return v, ok
}

// Put stores v as the value of the session of ctx. A request with no session
// starts one, with a new token and a cookie.
//
// At a login, call [Renew] first: Put keeps the token that the request
// carried.
//
// Put panics when ctx passed no session middleware, or when T is not the type
// of the middleware.
func Put[T any](ctx context.Context, v T) error {
	s := stateOf(ctx, "Put")
	if typ := reflect.TypeFor[T](); typ != s.m.typ {
		panic(fmt.Sprintf("session: Put[%v] on a session of %v", typ, s.m.typ))
	}
	if err := s.mustBeOpen(); err != nil {
		return err
	}
	if s.token == "" {
		now := time.Now()
		return s.save(ctx, NewToken(), v, now, s.m.expiry(now, now))
	}
	return s.save(ctx, s.token, v, s.created, s.expires)
}

// Renew moves the session of ctx to a new token, with its value, and deletes
// the record of the old one. The Lifetime starts again. Call it at a login and
// at any change of privilege. A request with no session has nothing to move,
// and Renew does nothing.
//
// Renew panics when ctx passed no session middleware.
func Renew(ctx context.Context) error {
	s := stateOf(ctx, "Renew")
	if err := s.mustBeOpen(); err != nil {
		return err
	}
	if s.token == "" {
		return nil
	}
	if err := s.m.store.Delete(ctx, s.token); err != nil {
		return fmt.Errorf("session: delete: %w", err)
	}
	now := time.Now()
	return s.save(ctx, NewToken(), s.data, now, s.m.expiry(now, now))
}

// Destroy deletes the record of the session of ctx and clears the cookie, as
// a logout does. A later [Put] in the same request starts a new session.
//
// Destroy panics when ctx passed no session middleware.
func Destroy(ctx context.Context) error {
	s := stateOf(ctx, "Destroy")
	if err := s.mustBeOpen(); err != nil {
		return err
	}
	if s.token != "" {
		if err := s.m.store.Delete(ctx, s.token); err != nil {
			return fmt.Errorf("session: delete: %w", err)
		}
	}
	if s.sent {
		s.clearCookie()
	}
	s.data, s.token, s.created, s.expires = nil, "", time.Time{}, time.Time{}
	return nil
}
