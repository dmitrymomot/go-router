package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
)

// SecureOmit turns one header off. Assign it to a field whose empty value
// means the default, the way a struct tag drops a field:
//
//	middleware.SecureConfig{FrameOptions: middleware.SecureOmit}
//
// A page that names its frame-ancestors in a Content-Security-Policy has no
// use for X-Frame-Options, and this is how it stops sending both.
const SecureOmit = "-"

// SecureConfig configures [SecureWithConfig].
type SecureConfig struct {
	// Skip passes a request straight to the next handler when it returns true.
	Skip func(c router.Context) bool

	// ContentTypeNosniff is the X-Content-Type-Options header. It defaults to
	// "nosniff", which stops a browser from guessing a media type that differs
	// from the one the answer declares.
	ContentTypeNosniff string

	// FrameOptions is the X-Frame-Options header. It defaults to "SAMEORIGIN",
	// which lets only this site frame the page.
	FrameOptions string

	// ContentSecurityPolicy is the policy of the page. An empty value sends no
	// header, because a policy that fits every application does not exist.
	ContentSecurityPolicy string

	// CSPReportOnly sends the policy as Content-Security-Policy-Report-Only,
	// which reports what a policy would have blocked and blocks nothing. Use
	// it to measure a new policy before it takes effect.
	CSPReportOnly bool

	// ReferrerPolicy is the Referrer-Policy header. It defaults to
	// "strict-origin-when-cross-origin", which sends the full URL to this
	// origin, the origin alone to another one, and nothing at all when the
	// answer leaves HTTPS.
	ReferrerPolicy string

	// HSTSMaxAge is how long a browser reaches this host over HTTPS alone.
	// Zero sends no header.
	//
	// Set it once the site is ready to serve every path over TLS forever: a
	// browser that saw the header refuses plaintext until it expires, and no
	// answer can take that back.
	HSTSMaxAge time.Duration

	// HSTSIncludeSubdomains extends HSTSMaxAge to every subdomain of this
	// host.
	HSTSIncludeSubdomains bool

	// HSTSPreload adds the preload token, which is what the browser preload
	// list asks for. It counts only together with HSTSIncludeSubdomains and a
	// max-age of at least a year.
	HSTSPreload bool
}

// Secure is [SecureWithConfig] with its default config: nosniff, SAMEORIGIN,
// strict-origin-when-cross-origin, no policy and no HSTS. It is a middleware
// itself, so it goes into Use without a call:
//
//	r.Use(middleware.Secure[Ctx])
func Secure[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return SecureWithConfig[C](SecureConfig{})(next)
}

// SecureWithConfig writes the security headers of an answer. It holds no
// state and reads nothing of the request but its scheme.
//
// The headers go out before the handler runs, so an answer that the handler
// wrote and an answer that an error produced carry the same ones.
//
// It does not send X-XSS-Protection. Every current browser ignores the header,
// and the filter it used to turn on introduced holes of its own, which is why
// the browsers dropped it. A Content-Security-Policy is what replaced it.
func SecureWithConfig[C router.Context](cfg SecureConfig) router.Middleware[C] {
	nosniff := setting(cfg.ContentTypeNosniff, "nosniff")
	frame := setting(cfg.FrameOptions, "SAMEORIGIN")
	referrer := setting(cfg.ReferrerPolicy, "strict-origin-when-cross-origin")

	csp := router.HeaderContentSecurityPolicy
	if cfg.CSPReportOnly {
		csp = router.HeaderContentSecurityPolicyReportOnly
	}

	// The value never changes, so it is built once rather than per request.
	hsts := ""
	if cfg.HSTSMaxAge > 0 {
		var b strings.Builder
		b.WriteString("max-age=")
		b.WriteString(strconv.Itoa(int(cfg.HSTSMaxAge.Seconds())))
		if cfg.HSTSIncludeSubdomains {
			b.WriteString("; includeSubDomains")
		}
		if cfg.HSTSPreload {
			b.WriteString("; preload")
		}
		hsts = b.String()
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}
			h := c.Response().Header()
			if nosniff != "" {
				h.Set(router.HeaderXContentTypeOptions, nosniff)
			}
			if frame != "" {
				h.Set(router.HeaderXFrameOptions, frame)
			}
			if referrer != "" {
				h.Set(router.HeaderReferrerPolicy, referrer)
			}
			if cfg.ContentSecurityPolicy != "" {
				h.Set(csp, cfg.ContentSecurityPolicy)
			}
			// HSTS over plaintext says nothing: a client that reads it there
			// reached the server over the very protocol the header forbids,
			// and an attacker who can read it can strip it.
			if hsts != "" && overTLS(c.Request()) {
				h.Set(router.HeaderStrictTransportSecurity, hsts)
			}
			return next(c)
		}
	}
}

// setting returns the value of a header field, its default when the field is
// empty, or an empty string when the field asks for no header at all.
func setting(v, fallback string) string {
	switch v {
	case "":
		return fallback
	case SecureOmit:
		return ""
	}
	return v
}

// overTLS reports whether the request reached the server over HTTPS. It reads
// the request the way [router.Base.Scheme] does: the TLS state of the
// connection first, then the X-Forwarded-Proto header that a proxy sets in
// front of a plain connection, which [RealIPWithConfig] fills in from a
// Forwarded header as well.
func overTLS(req *http.Request) bool {
	if req.TLS != nil {
		return true
	}
	proto, _, _ := strings.Cut(req.Header.Get(router.HeaderXForwardedProto), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}
