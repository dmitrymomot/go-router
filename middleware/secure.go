package middleware

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
)

// SecureOmit is the value that tells [SecureWithConfig] to send no header at
// all, where an empty string takes the default instead.
const SecureOmit = "-"

// SecureConfig configures [SecureWithConfig]. An empty string field takes its
// default, and [SecureOmit] drops the header: ContentTypeNosniff defaults to
// "nosniff", FrameOptions to "SAMEORIGIN", and ReferrerPolicy to
// "strict-origin-when-cross-origin".
//
// CrossOriginOpenerPolicy and CrossOriginResourcePolicy default to
// "same-origin", and [SecureOmit] drops them. CrossOriginEmbedderPolicy and
// PermissionsPolicy go out only when they are set. A site that serves images,
// scripts or fonts to other origins needs CrossOriginResourcePolicy of
// "cross-origin" or "same-site"; a site that opens a popup to another origin
// and talks to it needs CrossOriginOpenerPolicy of "same-origin-allow-popups"
// or [SecureOmit].
//
// ContentSecurityPolicy is sent only when it is set, and CSPReportOnly sends
// it as a report rather than a rule. CSP is the same policy written as fields,
// which the middleware checks and renders once; set one of the two. With
// CSPWithNonce, every request gets a fresh nonce, which goes into script-src
// and style-src and which [CSPNonce] reads, so an inline script or style that
// carries it runs without 'unsafe-inline'. CSPNonceContext, when set, runs once
// per request with that nonce and returns the request context the handler
// sees, so a template engine with a key of its own reads it too; pass
// templ.WithNonce and templ.GetNonce works, with no bridge middleware. The
// HSTS fields build Strict-Transport-Security, which goes out over HTTPS
// alone; HSTSMaxAge is a [time.Duration], so write 365*24*time.Hour.
type SecureConfig[C router.Context] struct {
	Skip                      func(c C) bool
	ContentTypeNosniff        string
	FrameOptions              string
	ContentSecurityPolicy     string
	CSP                       *CSPPolicy
	CSPNonceContext           func(ctx context.Context, nonce string) context.Context
	CSPWithNonce              bool
	CSPReportOnly             bool
	ReferrerPolicy            string
	CrossOriginOpenerPolicy   string
	CrossOriginEmbedderPolicy string
	CrossOriginResourcePolicy string
	PermissionsPolicy         string
	HSTSMaxAge                time.Duration
	HSTSIncludeSubdomains     bool
	HSTSPreload               bool
}

const (
	headerCrossOriginOpenerPolicy   = "Cross-Origin-Opener-Policy"
	headerCrossOriginEmbedderPolicy = "Cross-Origin-Embedder-Policy"
	headerCrossOriginResourcePolicy = "Cross-Origin-Resource-Policy"
	headerPermissionsPolicy         = "Permissions-Policy"
)

// Secure adds the security headers that suit any site: nosniff, SAMEORIGIN, a
// strict referrer policy, and a same-origin Cross-Origin-Opener-Policy and
// Cross-Origin-Resource-Policy. It sends no HSTS and no content security
// policy, because both need a decision that belongs to the site.
//
// See Order in the package doc for where it goes.
func Secure[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return SecureWithConfig(SecureConfig[C]{})(next)
}

// SecureWithConfig is [Secure] with a configuration.
//
// SecureWithConfig panics on an HSTSMaxAge under one second, which is the
// shape of a caller who wrote a number of seconds in place of a duration. It
// also panics when both ContentSecurityPolicy and CSP are set, on
// CSPWithNonce without CSP, on CSPNonceContext without CSPWithNonce, and on a CSP with no directive or with a source
// that is not one token, such as "https://a.example https://b.example" in
// one entry, or a keyword without its quotes, such as "self".
func SecureWithConfig[C router.Context](cfg SecureConfig[C]) router.Middleware[C] {
	nosniff := setting(cfg.ContentTypeNosniff, "nosniff")
	frame := setting(cfg.FrameOptions, "SAMEORIGIN")
	referrer := setting(cfg.ReferrerPolicy, "strict-origin-when-cross-origin")
	coop := setting(cfg.CrossOriginOpenerPolicy, "same-origin")
	corp := setting(cfg.CrossOriginResourcePolicy, "same-origin")
	coep := setting(cfg.CrossOriginEmbedderPolicy, "")
	permissions := setting(cfg.PermissionsPolicy, "")
	var policies [][2]string
	for _, p := range [][2]string{
		{headerCrossOriginOpenerPolicy, coop},
		{headerCrossOriginEmbedderPolicy, coep},
		{headerCrossOriginResourcePolicy, corp},
		{headerPermissionsPolicy, permissions},
	} {
		if p[1] != "" {
			policies = append(policies, p)
		}
	}

	switch {
	case cfg.CSP != nil && cfg.ContentSecurityPolicy != "":
		panic("middleware: SecureConfig sets both ContentSecurityPolicy and CSP; pick one")
	case cfg.CSPWithNonce && cfg.CSP == nil:
		panic("middleware: SecureConfig sets CSPWithNonce without CSP; the nonce goes into the typed policy")
	case cfg.CSPNonceContext != nil && !cfg.CSPWithNonce:
		panic("middleware: SecureConfig sets CSPNonceContext without CSPWithNonce; there is no nonce to pass")
	}
	policy := cfg.ContentSecurityPolicy
	var policyParts []string
	if cfg.CSP != nil {
		policy = cfg.CSP.render(cfg.CSPWithNonce)
		if cfg.CSPWithNonce {
			policyParts = strings.Split(policy, cspNonceSlot)
		}
	}

	csp := router.HeaderContentSecurityPolicy
	if cfg.CSPReportOnly {
		csp = router.HeaderContentSecurityPolicyReportOnly
	}

	hsts := ""
	if cfg.HSTSMaxAge > 0 {
		secs := int(cfg.HSTSMaxAge.Seconds())
		if secs < 1 {
			panic(fmt.Sprintf("middleware: SecureConfig.HSTSMaxAge is %s, under one second; "+
				"it is a time.Duration, so write 365*24*time.Hour and not the number of seconds",
				cfg.HSTSMaxAge))
		}
		var b strings.Builder
		b.WriteString("max-age=")
		b.WriteString(strconv.Itoa(secs))
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
			for _, p := range policies {
				h.Set(p[0], p[1])
			}
			switch {
			case policyParts != nil:
				nonce := newCSPNonce()
				h.Set(csp, strings.Join(policyParts, nonce))
				ctx := c.Request().Context()
				if cfg.CSPNonceContext != nil {
					ctx = cfg.CSPNonceContext(ctx, nonce)
				}
				// The key of CSPNonce goes on last, so a hook that returns a
				// context it did not derive from ctx cannot drop it.
				c.SetContext(context.WithValue(ctx, cspNonceKey{}, nonce))
			case policy != "":
				h.Set(csp, policy)
			}
			if hsts != "" && router.SchemeOf(c.Request()) == "https" {
				h.Set(router.HeaderStrictTransportSecurity, hsts)
			}
			return next(c)
		}
	}
}

func setting(v, fallback string) string {
	switch v {
	case "":
		return fallback
	case SecureOmit:
		return ""
	}
	return v
}
