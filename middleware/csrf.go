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

const CSRFTokenKey = "csrf_token"

const (
	DefaultCSRFCookieName = "_csrf"
	DefaultCSRFFormField  = "_csrf"
)

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

type CSRFConfig struct {
	Skip func(c router.Context) bool

	TokenSources []TokenSource

	CookieName string

	CookiePath string

	CookieDomain string

	CookieMaxAge time.Duration

	CookieSecure bool

	CookieHTTPOnly bool

	CookieSameSite http.SameSite

	TrustedOrigins []string

	AllowSecFetchSite func(c router.Context) (bool, error)
}

func CSRF[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return CSRFWithConfig[C](CSRFConfig{})(next)
}

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
