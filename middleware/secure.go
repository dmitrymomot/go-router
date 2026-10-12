package middleware

import (
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
// it as a report rather than a rule. The HSTS fields build
// Strict-Transport-Security, which goes out over HTTPS alone; HSTSMaxAge is a
// [time.Duration], so write 365*24*time.Hour.
type SecureConfig[C router.Context] struct {
	Skip                      func(c C) bool
	ContentTypeNosniff        string
	FrameOptions              string
	ContentSecurityPolicy     string
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
// shape of a caller who wrote a number of seconds in place of a duration.
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
			if cfg.ContentSecurityPolicy != "" {
				h.Set(csp, cfg.ContentSecurityPolicy)
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
