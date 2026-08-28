package middleware

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
)

const SecureOmit = "-"

type SecureConfig struct {
	Skip                  func(c router.Context) bool
	ContentTypeNosniff    string
	FrameOptions          string
	ContentSecurityPolicy string
	CSPReportOnly         bool
	ReferrerPolicy        string
	HSTSMaxAge            time.Duration
	HSTSIncludeSubdomains bool
	HSTSPreload           bool
}

func Secure[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return SecureWithConfig[C](SecureConfig{})(next)
}

func SecureWithConfig[C router.Context](cfg SecureConfig) router.Middleware[C] {
	nosniff := setting(cfg.ContentTypeNosniff, "nosniff")
	frame := setting(cfg.FrameOptions, "SAMEORIGIN")
	referrer := setting(cfg.ReferrerPolicy, "strict-origin-when-cross-origin")

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
