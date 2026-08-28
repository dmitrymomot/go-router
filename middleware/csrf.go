package middleware

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
)

// CSRFTokenKey is the context key under which [CSRFWithConfig] stores the
// token. Read it with [CSRFTokenFrom].
const CSRFTokenKey = "csrf_token"

// DefaultCSRFCookieName is the cookie that carries the token, and
// DefaultCSRFFormField is the form field that posts it back. They share a
// name, which is how the double submit pattern is usually spelled.
const (
	DefaultCSRFCookieName = "_csrf"
	DefaultCSRFFormField  = "_csrf"
)

// DefaultCSRFCookieMaxAge is the lifetime of the token cookie.
const DefaultCSRFCookieMaxAge = 24 * time.Hour

// csrfTokenBytes is the entropy of one token, 256 bits, which puts a guess out
// of reach.
const csrfTokenBytes = 32

// The failures that [CSRFWithConfig] reports.
var (
	errCrossSite        = router.ErrForbidden.WithMessage("cross-site request")
	errInvalidCSRFToken = router.ErrForbidden.WithMessage("invalid CSRF token")
)

// defaultCSRFSources is the source list of a config that names none.
var defaultCSRFSources = []TokenSource{
	FromHeader(router.HeaderXCSRFToken, ""),
	FromForm(DefaultCSRFFormField),
}

// CSRFConfig configures [CSRFWithConfig].
type CSRFConfig struct {
	// Skip passes a request straight to the next handler when it returns true.
	Skip func(c router.Context) bool

	// TokenSources are the places that carry the token of an unsafe request,
	// tried in order. They default to the X-CSRF-Token header and then the
	// form field that [DefaultCSRFFormField] names.
	TokenSources []TokenSource

	// CookieName is the cookie that carries the token. It defaults to
	// [DefaultCSRFCookieName].
	CookieName string

	// CookiePath is the path of that cookie. It defaults to "/", because a
	// cookie without a path belongs to the directory of the request that set
	// it and never reaches the rest of the site.
	CookiePath string

	// CookieDomain is the domain of that cookie. An empty domain binds it to
	// the host that set it, which is what one site wants; a domain shares the
	// cookie with every subdomain, and a subdomain that someone else holds is
	// a subdomain that sets the token.
	CookieDomain string

	// CookieMaxAge is the lifetime of that cookie. It defaults to
	// [DefaultCSRFCookieMaxAge].
	CookieMaxAge time.Duration

	// CookieSecure keeps the cookie off a plain connection. Turn it on in
	// production. A SameSite of [http.SameSiteNoneMode] turns it on by itself.
	CookieSecure bool

	// CookieHTTPOnly keeps the cookie away from scripts. Turn it on for a
	// server-rendered application, which reads the token with [CSRFTokenFrom]
	// and puts it in a hidden field, and leave it off for a client that reads
	// the cookie and sends the header itself.
	CookieHTTPOnly bool

	// CookieSameSite is the SameSite attribute of the cookie. The zero value
	// writes no attribute, which a current browser reads as Lax.
	CookieSameSite http.SameSite

	// TrustedOrigins are the origins that may send a cross-site request, each
	// of them a scheme and a host such as "https://app.example". A malformed
	// entry panics at construction, because an origin that never matches opens
	// nothing and says nothing about it.
	TrustedOrigins []string

	// AllowSecFetchSite decides a request from what the browser says about it.
	// It returns true to accept the request without a token, false and no
	// error to hand the request to the token check, and an error to refuse it.
	//
	// The default reads Sec-Fetch-Site: same-origin and none pass, any other
	// value is refused unless the Origin is one of TrustedOrigins, and a
	// request that carries no such header goes to the token check.
	AllowSecFetchSite func(c router.Context) (bool, error)
}

// CSRF is [CSRFWithConfig] with its default config. It is a middleware itself,
// so it goes into Use without a call:
//
//	r.Use(middleware.CSRF[Ctx])
//
// The defaults suit a server-rendered application behind TLS termination. Set
// CookieSecure and CookieHTTPOnly through [CSRFWithConfig] in production:
//
//	r.Use(middleware.CSRFWithConfig[Ctx](middleware.CSRFConfig{
//		CookieSecure:   true,
//		CookieHTTPOnly: true,
//	}))
func CSRF[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return CSRFWithConfig[C](CSRFConfig{})(next)
}

// CSRFWithConfig refuses a request that another site made the browser send.
//
// It reads Sec-Fetch-Site first. Every current browser sends that header, sets
// it from the request itself and lets no page touch it, so a request that it
// labels same-origin or none comes from this site or from the address bar and
// passes. A request that it labels anything else passes only when its Origin
// is one of TrustedOrigins.
//
// A client that sends no Sec-Fetch-Site falls back to the double submit
// cookie. The middleware writes a random token to a cookie, and an unsafe
// request has to send that same token back in a header or a form field. A page
// on another origin makes the browser send the cookie, but the same-origin
// policy keeps it from reading the value, so it cannot put the token in the
// request.
//
// A safe method, GET, HEAD, OPTIONS or TRACE, gets the cookie and the token
// and skips both checks. That is what lets a page render the token into its
// forms, and it keeps the automatic OPTIONS answer of the router working.
//
// It writes the cookie for a request that carries none, and leaves the token
// of one that does alone, so an answer to a request that already holds a token
// carries no Set-Cookie. An unsafe request without the cookie therefore takes
// a fresh token that it cannot have sent back, is refused, and carries the
// cookie that makes the retry work.
//
// Every answer that it handles carries Vary: Cookie, so that a shared cache
// cannot hand one user the token of another.
//
// Read the token in a handler or a template with [CSRFTokenFrom].
func CSRFWithConfig[C router.Context](cfg CSRFConfig) router.Middleware[C] {
	if len(cfg.TokenSources) == 0 {
		cfg.TokenSources = defaultCSRFSources
	}
	checkTokenSources("CSRFConfig", cfg.TokenSources)
	if cfg.CookieName == "" {
		cfg.CookieName = DefaultCSRFCookieName
	}
	if cfg.CookiePath == "" {
		cfg.CookiePath = "/"
	}
	if cfg.CookieMaxAge <= 0 {
		cfg.CookieMaxAge = DefaultCSRFCookieMaxAge
	}
	if cfg.CookieSameSite == http.SameSiteNoneMode {
		// A browser drops a SameSite=None cookie that is not secure, and a
		// CSRF middleware whose cookie never arrives refuses every request.
		cfg.CookieSecure = true
	}
	trusted := checkOrigins(cfg.TrustedOrigins)
	if cfg.AllowSecFetchSite == nil {
		cfg.AllowSecFetchSite = secFetchSite(trusted)
	}
	maxAge := int(cfg.CookieMaxAge.Seconds())

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			// A cache that keyed the answer on the URL alone would hand one
			// user a page that carries the token of another.
			c.Response().Header().Add(router.HeaderVary, router.HeaderCookie)

			token := csrfCookieToken(c.Request(), cfg.CookieName)
			if token == "" {
				token = newCSRFToken()
				http.SetCookie(c.Response(), &http.Cookie{
					Name:     cfg.CookieName,
					Value:    token,
					Path:     cfg.CookiePath,
					Domain:   cfg.CookieDomain,
					MaxAge:   maxAge,
					Secure:   cfg.CookieSecure,
					HttpOnly: cfg.CookieHTTPOnly,
					SameSite: cfg.CookieSameSite,
				})
			}
			c.Set(CSRFTokenKey, token)

			if csrfSafeMethod(c.Request().Method) {
				return next(c)
			}

			allowed, err := cfg.AllowSecFetchSite(c)
			if err != nil {
				return err
			}
			if !allowed && !csrfTokenMatches(c, cfg.TokenSources, token) {
				return errInvalidCSRFToken
			}
			return next(c)
		}
	}
}

// CSRFTokenFrom returns the token that [CSRFWithConfig] stored, or an empty
// string.
//
// A form posts it back in the hidden field that the default sources read:
//
//	<input type="hidden" name="_csrf" value={ middleware.CSRFTokenFrom(c) }/>
//
// A component receives the request state and not the application context, so
// inside a template it reaches the token through [router.FromContext]:
//
//	templ Form() {
//		if c, ok := router.FromContext(ctx); ok {
//			<input type="hidden" name="_csrf" value={ middleware.CSRFTokenFrom(c) }/>
//		}
//	}
func CSRFTokenFrom[C router.Context](c C) string {
	s, _ := c.Value(CSRFTokenKey).(string)
	return s
}

// csrfSafeMethod reports whether the method only reads. A safe request changes
// nothing, so it needs no token, and the router answers OPTIONS by itself.
func csrfSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

// csrfCookieToken returns the token that the cookie carries.
//
// A request that carries two cookies of the name carries none as far as this
// is concerned. A page on a sibling domain sets a cookie that the browser
// sends next to ours, and taking either of them would let that page pick the
// token that it then submits.
func csrfCookieToken(r *http.Request, name string) string {
	cookies := r.CookiesNamed(name)
	if len(cookies) != 1 {
		return ""
	}
	return cookies[0].Value
}

// csrfTokenMatches reports whether the request sends the token of the cookie
// back.
func csrfTokenMatches(c router.Context, sources []TokenSource, token string) bool {
	for sent := range readTokens(c, sources) {
		if SecureCompare(sent, token) {
			return true
		}
	}
	return false
}

// newCSRFToken returns a fresh token.
//
// It draws the bytes from crypto/rand and encodes them, so every token is as
// likely as every other one. A time-sortable identifier such as a UUID version
// 7 carries the clock in its high bits and hands away most of the value, which
// is why the request identifier of this package is one and a CSRF token is
// not.
func newCSRFToken() string {
	var b [csrfTokenBytes]byte
	//nolint:errcheck // crypto/rand.Read never fails; it crashes the program instead.
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// secFetchSite returns the default AllowSecFetchSite, which reads the
// Sec-Fetch-Site header and lets the trusted origins through.
func secFetchSite(trusted []string) func(router.Context) (bool, error) {
	return func(c router.Context) (bool, error) {
		switch c.Request().Header.Get(router.HeaderSecFetchSite) {
		case "same-origin", "none":
			// The site asked itself, or the user did, from a bookmark or the
			// address bar.
			return true, nil
		case "":
			// The client is not a current browser, or something on the way
			// stripped the header, so the token decides.
			return false, nil
		default:
			// The rest is same-site and cross-site. A sibling subdomain is
			// another origin, and one that someone else holds is one that
			// posts, so it passes only by name.
			origin := c.Request().Header.Get(router.HeaderOrigin)
			if origin != "" && slices.ContainsFunc(trusted, func(t string) bool {
				return strings.EqualFold(t, origin)
			}) {
				return true, nil
			}
			return false, errCrossSite
		}
	}
}

// checkOrigins returns the trusted origins in the form that the comparison
// reads, and panics on an entry that is not an origin. An origin with a path
// or a trailing slash matches nothing, so it is a typo that would otherwise
// stay silent until a client hit it.
func checkOrigins(origins []string) []string {
	out := make([]string, len(origins))
	for i, origin := range origins {
		canonical, ok := originOf(origin)
		if !ok {
			panic(fmt.Sprintf("middleware: CSRFConfig has a malformed trusted origin %q, "+
				"which reads as a scheme and a host, such as %q", origin, "https://app.example"))
		}
		out[i] = canonical
	}
	return out
}
