package middleware

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
)

// CSRFTokenKey is where [CSRF] stores the token on the context.
const CSRFTokenKey = "csrf_token"

// The cookie and the form field that [CSRF] uses when the configuration names
// none.
const (
	DefaultCSRFCookieName = "_csrf"
	DefaultCSRFFormField  = "_csrf"
)

// DefaultCSRFCookieMaxAge is how long the CSRF cookie lives when
// CSRFConfig.CookieMaxAge is zero or less.
const DefaultCSRFCookieMaxAge = 24 * time.Hour

const csrfTokenBytes = 32

var (
	errCrossSite        = router.ErrForbidden.WithMessage("cross-site request")
	errInvalidCSRFToken = router.ErrForbidden.WithMessage("invalid CSRF token")
)

var defaultCSRFSources = []TokenSource{
	FromHeader(router.HeaderXCSRFToken, ""),
	FromForm(DefaultCSRFFormField),
}

// CSRFConfig configures [CSRFWithConfig].
//
// TokenSources say where the token of an unsafe request may come from, and an
// empty list reads the X-CSRF-Token header and the "_csrf" form field. The
// Cookie fields shape the cookie that carries the token; CookieHTTPOnly has to
// stay false for a script to read it, and a template that renders the token
// into the form does not need it.
//
// TrustedOrigins names the origins that may post cross-site, each with a
// scheme and a host. AllowSecFetchSite replaces the Sec-Fetch-Site check
// entirely.
type CSRFConfig struct {
	Skip              func(c router.Context) bool
	TokenSources      []TokenSource
	CookieName        string
	CookiePath        string
	CookieDomain      string
	CookieMaxAge      time.Duration
	CookieSecure      bool
	CookieHTTPOnly    bool
	CookieSameSite    http.SameSite
	TrustedOrigins    []string
	AllowSecFetchSite func(c router.Context) (bool, error)
}

// CSRF issues a token, puts it in a cookie and on the context, and refuses an
// unsafe request that does not send it back. GET, HEAD, OPTIONS and TRACE pass
// through and still get a token.
//
// A request that Sec-Fetch-Site marks as same-origin passes without a token,
// which is what lets a browser that sends the header carry an ordinary form.
// One that marks it cross-site is refused outright unless its origin is
// trusted. A request with no such header needs the token.
//
// [CSRFTokenFrom] reads the token for a template.
func CSRF[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return CSRFWithConfig[C](CSRFConfig{})(next)
}

// CSRFWithConfig is [CSRF] with a configuration.
//
// CSRFWithConfig panics on a nil token source, on more than
// [MaxTokenSources] of them, and on a trusted origin it cannot parse.
func CSRFWithConfig[C router.Context](cfg CSRFConfig) router.Middleware[C] {
	if len(cfg.TokenSources) == 0 {
		cfg.TokenSources = defaultCSRFSources
	}
	cfg.TokenSources = slices.Clone(cfg.TokenSources)
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
	alwaysSecure := cfg.CookieSecure
	if cfg.CookieSameSite == http.SameSiteNoneMode {
		alwaysSecure = true
	}
	trusted := checkTrustedOrigins(cfg.TrustedOrigins)
	if cfg.AllowSecFetchSite == nil {
		cfg.AllowSecFetchSite = secFetchSite(trusted)
	}
	maxAge := int(cfg.CookieMaxAge.Seconds())

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			router.AddVary(c.Response().Header(), router.HeaderCookie)

			token := csrfCookieToken(c.Request(), cfg.CookieName)
			if token == "" {
				token = newCSRFToken()
				http.SetCookie(c.Response(), &http.Cookie{
					Name:     cfg.CookieName,
					Value:    token,
					Path:     cfg.CookiePath,
					Domain:   cfg.CookieDomain,
					MaxAge:   maxAge,
					Secure:   alwaysSecure || router.SchemeOf(c.Request()) == "https",
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

// CSRFTokenFrom reports the token that [CSRF] stored, for a template to render
// into a hidden form field. It reports "" when the middleware did not run.
func CSRFTokenFrom[C router.Context](c C) string {
	s, _ := c.Value(CSRFTokenKey).(string)
	return s
}

func csrfSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

func csrfCookieToken(r *http.Request, name string) string {
	cookies := r.CookiesNamed(name)
	if len(cookies) != 1 {
		return ""
	}
	return cookies[0].Value
}

func csrfTokenMatches(c router.Context, sources []TokenSource, token string) bool {
	for sent := range readTokens(c, sources) {
		if SecureCompare(sent, token) {
			return true
		}
	}
	return false
}

func newCSRFToken() string {
	var b [csrfTokenBytes]byte
	//nolint:errcheck // crypto/rand.Read never fails; it crashes the program instead.
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func secFetchSite(trusted []string) func(router.Context) (bool, error) {
	return func(c router.Context) (bool, error) {
		switch c.Request().Header.Get(router.HeaderSecFetchSite) {
		case "same-origin", "none":
			return true, nil
		case "":
			return false, nil
		default:
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

func checkTrustedOrigins(origins []string) []string {
	out := make([]string, len(origins))
	for i, origin := range origins {
		out[i] = checkOrigin("CSRFConfig.TrustedOrigins", origin, "")
	}
	return out
}
