package router

import (
	"net/http"
	"time"
)

// Cookie reports the value of the cookie called name, or "" when the request
// carries none or carries it empty. A request that sends the name twice has
// the first one read. See [github.com/dmitrymomot/go-router/cookie.Codec.Get]
// for a cookie the client cannot forge.
func (b *Base) Cookie(name string) string {
	c, err := b.req.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// NewCookie builds a cookie the way the router builds its own: Path "/",
// HttpOnly, SameSite=Lax, and Secure when [Base.Scheme] reports https. Change
// any field before [Base.SetCookie] or
// [github.com/dmitrymomot/go-router/cookie.Codec.Set] writes it.
//
// maxAge is how long the browser keeps the cookie, rounded up to whole
// seconds. Zero makes a session cookie, which lasts until the browser closes,
// and a negative maxAge makes one that deletes the cookie at once.
//
// The value goes out as net/http writes it, which drops a semicolon, a quote,
// a backslash or a control byte. Escape a value that may hold one.
func (b *Base) NewCookie(name, value string, maxAge time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   cookieMaxAge(maxAge),
		Secure:   b.Scheme() == "https",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

// A sub-second lifetime rounds up, so it never turns into a session cookie.
func cookieMaxAge(d time.Duration) int {
	switch {
	case d < 0:
		return -1
	case d%time.Second != 0:
		return int(d/time.Second) + 1
	default:
		return int(d / time.Second)
	}
}

// SetCookie adds a Set-Cookie header to the response. [Base.NewCookie] builds
// one with the router's defaults. A cookie whose name is not a valid token
// writes nothing, as with [http.SetCookie].
func (b *Base) SetCookie(c *http.Cookie) { http.SetCookie(b.res, c) }

// ClearCookie tells the browser to drop the cookie called name, as
// [Base.NewCookie] built it. It writes whether or not the request carries the
// cookie. A cookie set with another Path or with a Domain is dropped only by
// one with the same Path and Domain: take NewCookie(name, "", -1) and set them.
func (b *Base) ClearCookie(name string) { b.SetCookie(b.NewCookie(name, "", -1)) }
