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

// The cookies and the form field that [CSRF] uses when the configuration names
// none. The cookie over HTTPS carries the __Host- prefix, so the browser keeps
// it to this host alone and a sibling subdomain cannot set it.
const (
	DefaultCSRFCookieName     = "_csrf"
	DefaultCSRFHostCookieName = "__Host-csrf"
	DefaultCSRFFormField      = "_csrf"
)

// DefaultCSRFCookieMaxAge is how long the CSRF cookie lives when
// CSRFConfig.CookieMaxAge is zero.
const DefaultCSRFCookieMaxAge = 24 * time.Hour

const csrfTokenBytes = 32

// csrfTokenLength is the length of the tokens [CSRF] issues. A cookie of any
// other shape was not issued here.
var csrfTokenLength = base64.RawURLEncoding.EncodedLen(csrfTokenBytes)

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
// Sources say where the token of an unsafe request may come from, and an empty
// list reads the X-CSRF-Token header and the "_csrf" form field.
//
// The Cookie fields shape the cookie that carries the token. An empty
// CookieName takes [DefaultCSRFHostCookieName] over HTTPS and
// [DefaultCSRFCookieName] over HTTP; the __Host- cookie always goes out with
// Secure, the path "/" and no domain, so CookiePath and CookieDomain need a
// CookieName of their own. CookieMaxAge is how long the cookie lives, and zero
// takes [DefaultCSRFCookieMaxAge]. CookieHTTPOnly has to stay false for a
// script to read the cookie, and a template that renders the token into the
// form does not need it.
//
// TrustedOrigins names the origins that may post from another site, each with
// a scheme and a host. AllowSecFetchSite replaces the Sec-Fetch-Site check
// entirely.
type CSRFConfig[C router.Context] struct {
	Skip              func(c C) bool
	Sources           []TokenSource
	CookieName        string
	CookiePath        string
	CookieDomain      string
	CookieMaxAge      time.Duration
	CookieSecure      bool
	CookieHTTPOnly    bool
	CookieSameSite    http.SameSite
	TrustedOrigins    []string
	AllowSecFetchSite func(c C) (bool, error)
}

// CSRF issues a token, puts it in a cookie and on the context, and refuses an
// unsafe request that does not send it back. GET, HEAD, OPTIONS, TRACE and
// QUERY pass through and still get a token. A cookie that does not hold a
// token of the shape CSRF issues counts as absent, and a new token replaces
// it.
//
// A request that Sec-Fetch-Site marks as same-origin passes without a token,
// which is what lets a browser that sends the header carry an ordinary form.
// One that marks it cross-site is refused outright unless its origin is
// trusted. A same-site request, and one with no such header, needs the token.
//
// [CSRFTokenFrom] reads the token for a template.
//
// See Order in the package doc for where it goes.
func CSRF[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return CSRFWithConfig(CSRFConfig[C]{})(next)
}

// CSRFWithConfig is [CSRF] with a configuration.
//
// CSRFWithConfig panics on a nil token source, on more than [MaxTokenSources]
// of them, on a trusted origin it cannot parse, on a negative CookieMaxAge,
// and on a CookiePath or CookieDomain without a CookieName.
func CSRFWithConfig[C router.Context](cfg CSRFConfig[C]) router.Middleware[C] {
	if len(cfg.Sources) == 0 {
		cfg.Sources = defaultCSRFSources
	}
	cfg.Sources = slices.Clone(cfg.Sources)
	checkTokenSources("CSRFConfig", cfg.Sources)
	if cfg.CookieMaxAge < 0 {
		panic("middleware: CSRFWithConfig needs a CookieMaxAge of zero or more")
	}
	if cfg.CookieName == "" && (cfg.CookieDomain != "" || cfg.CookiePath != "" && cfg.CookiePath != "/") {
		panic("middleware: CSRFConfig sets a CookiePath or a CookieDomain without a CookieName; " +
			"the default cookie over HTTPS is " + DefaultCSRFHostCookieName + ", which takes neither")
	}
	if cfg.CookiePath == "" {
		cfg.CookiePath = "/"
	}
	if cfg.CookieMaxAge == 0 {
		cfg.CookieMaxAge = DefaultCSRFCookieMaxAge
	}
	alwaysSecure := cfg.CookieSecure || cfg.CookieSameSite == http.SameSiteNoneMode
	trusted, _ := checkOrigins("CSRFConfig.TrustedOrigins", cfg.TrustedOrigins, false, "")
	if cfg.AllowSecFetchSite == nil {
		cfg.AllowSecFetchSite = secFetchSite[C](trusted)
	}
	maxAge := int(cfg.CookieMaxAge.Seconds())

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			router.AddVary(c.Response().Header(), router.HeaderCookie)

			https := router.SchemeOf(c.Request()) == "https"
			name := cfg.CookieName
			if name == "" {
				name = DefaultCSRFCookieName
				if https {
					name = DefaultCSRFHostCookieName
				}
			}
			token := csrfCookieToken(c.Request(), name)
			if token == "" {
				token = newCSRFToken()
				http.SetCookie(c.Response(), &http.Cookie{
					Name:     name,
					Value:    token,
					Path:     cfg.CookiePath,
					Domain:   cfg.CookieDomain,
					MaxAge:   maxAge,
					Secure:   alwaysSecure || https,
					HttpOnly: cfg.CookieHTTPOnly,
					SameSite: cfg.CookieSameSite,
				})
			}
			c.Set(CSRFTokenKey, token)

			if isSafeMethod(c.Request().Method) {
				return next(c)
			}

			allowed, err := cfg.AllowSecFetchSite(c)
			if err != nil {
				return err
			}
			if !allowed && !csrfTokenMatches(c, cfg.Sources, token) {
				return errInvalidCSRFToken
			}
			return next(c)
		}
	}
}

// CSRFTokenFrom reports the token that [CSRF] stored, for a template to render
// into a hidden form field. It reports "" when the middleware did not run.
func CSRFTokenFrom(c router.Context) string {
	s, _ := c.Value(CSRFTokenKey).(string)
	return s
}

// csrfCookieToken reports the token of the one cookie named name, or "" when
// there is no such cookie, more than one, or one that CSRF did not issue.
func csrfCookieToken(r *http.Request, name string) string {
	cookies := r.CookiesNamed(name)
	if len(cookies) != 1 || !isCSRFToken(cookies[0].Value) {
		return ""
	}
	return cookies[0].Value
}

// isCSRFToken reports whether s has the length and the base64url alphabet of
// the tokens [newCSRFToken] makes.
func isCSRFToken(s string) bool {
	if len(s) != csrfTokenLength {
		return false
	}
	for i := range len(s) {
		switch b := s[i]; {
		case 'A' <= b && b <= 'Z', 'a' <= b && b <= 'z', '0' <= b && b <= '9', b == '-', b == '_':
		default:
			return false
		}
	}
	return true
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

// secFetchSite lets a same-origin request through, and a request from a
// trusted origin. It refuses any other cross-site request outright, and leaves
// a same-site request, and one without the header, to the token.
func secFetchSite[C router.Context](trusted []string) func(C) (bool, error) {
	return func(c C) (bool, error) {
		site := c.Request().Header.Get(router.HeaderSecFetchSite)
		switch site {
		case "same-origin", "none":
			return true, nil
		case "":
			return false, nil
		}
		origin := c.Request().Header.Get(router.HeaderOrigin)
		if origin != "" && slices.ContainsFunc(trusted, func(t string) bool {
			return strings.EqualFold(t, origin)
		}) {
			return true, nil
		}
		if site == "same-site" {
			return false, nil
		}
		return false, errCrossSite
	}
}
