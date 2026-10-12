package middleware

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
)

// CSRFTokenKey is the context key under which [CSRF] keeps the state that
// [CSRFTokenFrom] reads. The value is not a string; call [CSRFTokenFrom]. The
// "middleware." prefix keeps it apart from the keys of the application.
const CSRFTokenKey = "middleware.csrf_token"

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

// CSRF issues a token in a cookie, and refuses an unsafe request that does not
// send it back. GET, HEAD, OPTIONS, TRACE and QUERY pass through. The cookie
// goes out, and Cookie joins Vary, only when something needs the token: a call
// of [CSRFTokenFrom] during the request, or the check of an unsafe request. A
// safe request that never reads the token gets neither, so a page that has no
// form stays cacheable. A cookie that does not hold a token of the shape CSRF
// issues counts as absent, and a new token replaces it.
//
// A request that Sec-Fetch-Site marks as same-origin passes without a token,
// which is what lets a browser that sends the header carry an ordinary form.
// One that marks it cross-site is refused outright unless its origin is
// trusted. A same-site request needs the token, and only the cookie
// [DefaultCSRFHostCookieName], which CSRF uses over HTTPS when the
// configuration names no cookie, may carry it: a sibling subdomain can plant
// any other cookie for the parent domain, so over plain HTTP, or with a
// CookieName without the __Host- prefix, a same-site request is refused. A
// request with no such header needs the token.
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
		cfg.AllowSecFetchSite = secFetchSite[C](trusted, cfg.CookieName)
	}
	cookie := &csrfCookie{
		path:     cfg.CookiePath,
		domain:   cfg.CookieDomain,
		maxAge:   int(cfg.CookieMaxAge.Seconds()),
		httpOnly: cfg.CookieHTTPOnly,
		sameSite: cfg.CookieSameSite,
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			https := router.SchemeOf(c.Request()) == "https"
			state := &csrfState{
				res:    c.Response(),
				req:    c.Request(),
				name:   csrfCookieName(cfg.CookieName, https),
				cookie: cookie,
				secure: alwaysSecure || https,
			}
			c.Set(CSRFTokenKey, state)

			if isSafeMethod(c.Request().Method) {
				return next(c)
			}

			token := state.token()
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

// CSRFTokenFrom reports the token of the request for a template to render into
// a hidden form field, or "" when [CSRF] did not run. The first call issues the
// cookie and adds Cookie to Vary, so call it before the handler writes the
// answer: a cookie set after that never reaches the client.
//
// Each call returns a new string, the token under a random mask, so that the
// bytes of a page that carries it differ from one render to the next and a
// compression oracle cannot learn it (BREACH). [CSRF] unmasks what comes back,
// and still takes the bare token that the cookie holds, which a script may
// read and send in a header.
func CSRFTokenFrom(c router.Context) string {
	s, ok := c.Value(CSRFTokenKey).(*csrfState)
	if !ok {
		return ""
	}
	return maskCSRFToken(s.token())
}

// csrfCookie holds the attributes of the cookie that carries the token.
type csrfCookie struct {
	path     string
	domain   string
	maxAge   int
	httpOnly bool
	sameSite http.SameSite
}

// csrfState is what [CSRF] keeps on the context. It reads the cookie, or issues
// one, the first time something needs the token.
type csrfState struct {
	res    http.ResponseWriter
	req    *http.Request
	name   string
	cookie *csrfCookie
	secure bool
	value  string
}

// token reports the bare token of the request. On the first call it adds Cookie
// to Vary, and sets the cookie unless the request carries a good one.
func (s *csrfState) token() string {
	if s.value != "" {
		return s.value
	}
	router.AddVary(s.res.Header(), router.HeaderCookie)
	s.value = csrfCookieToken(s.req, s.name)
	if s.value == "" {
		s.value = newCSRFToken()
		http.SetCookie(s.res, &http.Cookie{
			Name:     s.name,
			Value:    s.value,
			Path:     s.cookie.path,
			Domain:   s.cookie.domain,
			MaxAge:   s.cookie.maxAge,
			Secure:   s.secure,
			HttpOnly: s.cookie.httpOnly,
			SameSite: s.cookie.sameSite,
		})
	}
	return s.value
}

// csrfMaskedLength is the length of a masked token: the pad and the masked
// token side by side.
var csrfMaskedLength = base64.RawURLEncoding.EncodedLen(2 * csrfTokenBytes)

// maskCSRFToken XORs the token with a fresh random pad of its own length and
// reports the pad followed by the result, in base64url.
func maskCSRFToken(token string) string {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != csrfTokenBytes {
		return token
	}
	var out [2 * csrfTokenBytes]byte
	//nolint:errcheck // crypto/rand.Read never fails; it crashes the program instead.
	rand.Read(out[:csrfTokenBytes])
	for i := range csrfTokenBytes {
		out[csrfTokenBytes+i] = out[i] ^ raw[i]
	}
	return base64.RawURLEncoding.EncodeToString(out[:])
}

// csrfSentMatches reports whether sent is the token, bare or masked.
func csrfSentMatches(sent, token string) bool {
	if len(sent) != csrfMaskedLength {
		return SecureCompare(sent, token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(sent)
	want, werr := base64.RawURLEncoding.DecodeString(token)
	if err != nil || werr != nil || len(raw) != 2*csrfTokenBytes || len(want) != csrfTokenBytes {
		return false
	}
	var unmasked [csrfTokenBytes]byte
	for i := range csrfTokenBytes {
		unmasked[i] = raw[i] ^ raw[csrfTokenBytes+i]
	}
	return subtle.ConstantTimeCompare(unmasked[:], want) == 1
}

// csrfCookieName reports the name of the cookie in use: the configured one,
// or the default for the scheme.
func csrfCookieName(configured string, https bool) string {
	switch {
	case configured != "":
		return configured
	case https:
		return DefaultCSRFHostCookieName
	}
	return DefaultCSRFCookieName
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
		if csrfSentMatches(sent, token) {
			return true
		}
	}
	return false
}

// hostOnlyCSRFCookie reports whether the cookie in use carries the __Host-
// prefix, which the browser keeps to this host, so no other host can set it.
func hostOnlyCSRFCookie(r *http.Request, configured string) bool {
	https := router.SchemeOf(r) == "https"
	return https && strings.HasPrefix(csrfCookieName(configured, https), hostCookiePrefix)
}

const hostCookiePrefix = "__Host-"

func newCSRFToken() string {
	var b [csrfTokenBytes]byte
	//nolint:errcheck // crypto/rand.Read never fails; it crashes the program instead.
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// secFetchSite lets a same-origin request through, and a request from a
// trusted origin. It refuses any other cross-site request outright, and leaves
// one without the header to the token. A same-site request goes to the token
// only when the cookie is a __Host- one over HTTPS, since a sibling subdomain
// can plant any other cookie for the parent domain; otherwise it is refused.
func secFetchSite[C router.Context](trusted []string, cookieName string) func(C) (bool, error) {
	return func(c C) (bool, error) {
		site := c.Request().Header.Get(router.HeaderSecFetchSite)
		switch site {
		case "same-origin", "none":
			return true, nil
		case "":
			return false, nil
		}
		origin := c.Request().Header.Get(router.HeaderOrigin)
		if origin != "" && sameOrigin(trusted, origin) {
			return true, nil
		}
		if site == "same-site" && hostOnlyCSRFCookie(c.Request(), cookieName) {
			return false, nil
		}
		return false, errCrossSite
	}
}
