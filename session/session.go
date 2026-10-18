// Package session carries the session of a signed-in user: a random token in
// a cookie names a row that the application keeps, such as a row of its
// sessions table, and the middleware puts that row on the request context.
//
// The package owns the mechanics: it makes the token, sets and clears the
// cookie, hashes the token of each request and asks the application for the
// row. The application owns the row: its columns, its tenant, the device it
// records, and the queries that list or revoke sessions. Only the hash of a
// token reaches the database, so a copy of the table opens no session.
//
//	sessions := session.New[*Context](session.Config[Row]{
//		Find: store.SessionByHash, // SELECT ... WHERE token_hash = $1 AND expires_at > now()
//	})
//	r.Use(sessions.Middleware())
//
//	func login(c *Context) error {
//		... check the credentials ...
//		tok := session.NewToken()
//		row, err := c.Store.CreateSession(c, account.ID, tok.Hash)
//		if err != nil {
//			return err
//		}
//		if err := sessions.Start(c, tok, row.ExpiresAt); err != nil {
//			return err
//		}
//		return c.Redirect(http.StatusSeeOther, "/")
//	}
//
// A session exists only once a user signed in. Each sign-in makes a new token
// and a new row, so there is nothing to rotate, and a token planted in the
// browser before it names nothing after it. A sign-in whose answer never
// reaches the browser leaves a row that no cookie names, and that row expires.
// State of a visitor, such as a cart or a flash message, belongs in a signed
// cookie of package cookie.
//
// To sign out, the application deletes the row and calls [Sessions.End]. A row
// that is gone ends its session at the next request, so deleting the rows of a
// user signs that user out on every device.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
)

var (
	// ErrNotFound is what Config.Find reports for a hash that names no live
	// session. The middleware then clears the cookie, and the request passes
	// as a visitor.
	ErrNotFound = errors.New("session: no such session")

	// ErrCommitted is what [Sessions.Start] and [Sessions.End] report once the
	// header of the response went out, since the cookie can no longer change.
	ErrCommitted = errors.New("session: the response is already committed")
)

const (
	// DefaultCookie is the name of the session cookie over HTTP, and
	// DefaultHostCookie its name over HTTPS. The __Host- prefix makes the
	// browser refuse the cookie unless it is Secure, at Path "/" and with no
	// Domain, so neither a parent domain nor a sibling subdomain can set a
	// cookie of that name to stand in front of it.
	DefaultCookie     = "session"
	DefaultHostCookie = "__Host-session"
)

// Token is the secret of a new session and its hash. Build one with
// [NewToken].
type Token struct {
	// Secret goes in the cookie, and never in the database.
	Secret string
	// Hash goes in the database: the middleware hashes the cookie of each
	// request the same way and passes the result to Config.Find.
	Hash []byte
}

// NewToken reports a new token: 32 bytes from crypto/rand as the secret, in
// unpadded base64url, and the SHA-256 of that secret as the hash. A secret of
// 256 random bits needs no slow hash: no one can guess it.
func NewToken() Token {
	var b [32]byte
	//nolint:errcheck // crypto/rand.Read never fails; it crashes the program.
	rand.Read(b[:])
	secret := base64.RawURLEncoding.EncodeToString(b[:])
	return Token{Secret: secret, Hash: HashToken(secret)}
}

// HashToken reports the hash of the secret of a token, as the middleware
// computes it for Config.Find.
func HashToken(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// Config configures [New]. S is the session row of the application.
//
// Find is required. It reports the row whose token has hash, and
// [ErrNotFound] for a hash that names no live session: one that never
// existed, was deleted, or expired. Find checks the expiry, as a query with
// "expires_at > now()" does. Any other error stops the request: a
// *router.HTTPError goes to the error handler as it stands, and anything else
// as a 500. Find runs with the handler context, so it reads the tenant of the
// request, and it must be safe for concurrent use.
//
// Touch, when set, runs after Find on each request that carries a session,
// to record the last use or to move a rolling expiry forward. It decides
// itself how often to write, as an UPDATE with "WHERE last_used_at < now() -
// interval '5 minutes'" does. Its errors stop the request as those of Find do.
//
// Cookie names the cookie. When it is empty, the cookie is [DefaultHostCookie]
// over HTTPS and [DefaultCookie] over HTTP, where a browser drops a Secure
// cookie, as router.Base.Scheme decides. The middleware reads the first cookie
// of the name that the request carries and no other. Under the __Host- prefix
// that cookie is the one this host set. Over HTTP, and under a name of your
// own without the prefix, a parent domain can set a cookie of the same name
// that the browser may send first; serve HTTPS, or keep the parent domain to
// hosts you trust.
type Config[S any] struct {
	Find   func(ctx context.Context, hash []byte) (S, error)
	Touch  func(ctx context.Context, s S) error
	Cookie string
}

// Sessions carries the session row of type S for a router whose context type
// is C. Build one with [New]; the zero Sessions is invalid. A Sessions is safe
// for concurrent use.
type Sessions[C router.Context, S any] struct {
	find   func(ctx context.Context, hash []byte) (S, error)
	touch  func(ctx context.Context, s S) error
	cookie string
}

// New builds a [Sessions] from cfg. Name the context type and let the row type
// follow from cfg, as in session.New[*Context](cfg).
//
// New panics if Find is nil or the Cookie is not a valid cookie name.
func New[C router.Context, S any](cfg Config[S]) *Sessions[C, S] {
	if cfg.Find == nil {
		panic("session: New needs a Find function")
	}
	//nolint:gosec // A cookie with only a name, to check that name.
	if cfg.Cookie != "" && (&http.Cookie{Name: cfg.Cookie}).Valid() != nil {
		panic(fmt.Sprintf("session: New cannot name a cookie %q", cfg.Cookie))
	}
	return &Sessions[C, S]{find: cfg.Find, touch: cfg.Touch, cookie: cfg.Cookie}
}

// Middleware puts the session row of each request on the request context,
// where [Sessions.From] reads it. A request with no cookie passes as a
// visitor, and so does one whose cookie names no live session, which the
// middleware also clears. Every response of the scope names Cookie in Vary.
func (s *Sessions[C, S]) Middleware() router.Middleware[C] {
	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			router.AddVary(c.Response().Header(), router.HeaderCookie)

			cookies := c.Request().CookiesNamed(s.cookieName(c))
			if len(cookies) == 0 || cookies[0].Value == "" {
				return next(c)
			}
			row, err := s.find(c, HashToken(cookies[0].Value))
			if errors.Is(err, ErrNotFound) {
				s.setCookie(c, "", -1)
				return next(c)
			}
			if err != nil {
				return stop(err, "find")
			}
			if s.touch != nil {
				if err := s.touch(c, row); err != nil {
					return stop(err, "touch")
				}
			}
			c.SetContext(context.WithValue(c.Request().Context(), ctxKey[S]{}, &entry[S]{row: row}))
			return next(c)
		}
	}
}

// stop turns an error of Find or Touch into the error of the request.
func stop(err error, step string) error {
	if _, ok := errors.AsType[*router.HTTPError](err); ok {
		return err
	}
	return router.ErrInternalServerError.WithError(fmt.Errorf("session: %s: %w", step, err))
}

// Start sets the cookie of a new session: the secret of t, until expires.
// The application stores t.Hash in the row first. A zero expires makes a
// cookie that lasts until the browser closes. A Set-Cookie of the session
// cookie that this response already carries is replaced.
//
// Start reports [ErrCommitted] once the header went out. It panics if t has
// no secret.
func (s *Sessions[C, S]) Start(c router.Context, t Token, expires time.Time) error {
	if t.Secret == "" {
		panic("session: Start needs a token from NewToken")
	}
	if c.Response().Committed {
		return ErrCommitted
	}
	var maxAge time.Duration
	if !expires.IsZero() {
		maxAge = max(time.Until(expires), time.Second)
	}
	s.setCookie(c, t.Secret, maxAge)
	return nil
}

// End clears the session cookie, and [Sessions.From] reports false for the
// rest of the request. The application deletes the row itself, before or
// after, in its own transaction.
//
// End reports [ErrCommitted] once the header went out.
func (s *Sessions[C, S]) End(c router.Context) error {
	if c.Response().Committed {
		return ErrCommitted
	}
	s.setCookie(c, "", -1)
	if e, ok := c.Value(ctxKey[S]{}).(*entry[S]); ok {
		e.ended = true
	}
	return nil
}

// From reports the session row that ctx carries. ok is false for a visitor,
// and after [Sessions.End].
func (s *Sessions[C, S]) From(ctx context.Context) (S, bool) {
	return From[S](ctx)
}

// cookieName reports the name of the session cookie of the request of c.
func (s *Sessions[C, S]) cookieName(c router.Context) string {
	switch {
	case s.cookie != "":
		return s.cookie
	case router.SchemeOf(c.Request()) == "https":
		return DefaultHostCookie
	default:
		return DefaultCookie
	}
}

// setCookie writes the session cookie, in place of one that an earlier call
// of this request wrote.
func (s *Sessions[C, S]) setCookie(c router.Context, value string, maxAge time.Duration) {
	name := s.cookieName(c)
	// The context of a router always carries its Base.
	b, _ := router.FromContext(c)
	line := b.NewCookie(name, value, maxAge).String()

	h := c.Response().Header()
	prefix := name + "="
	lines := h.Values(router.HeaderSetCookie)
	kept := lines[:0:0]
	for _, l := range lines {
		if !strings.HasPrefix(l, prefix) {
			kept = append(kept, l)
		}
	}
	h[router.HeaderSetCookie] = append(kept, line)
}

// entry holds the row of a request, and whether End ended it.
type entry[S any] struct {
	row   S
	ended bool
}

// ctxKey carries the session row of type S. Each row type has a key of its
// own.
type ctxKey[S any] struct{}

// From reports the session row of type S that ctx carries. ok is false for a
// visitor, and after [Sessions.End].
func From[S any](ctx context.Context) (S, bool) {
	e, ok := ctx.Value(ctxKey[S]{}).(*entry[S])
	if !ok || e.ended {
		var zero S
		return zero, false
	}
	return e.row, true
}
