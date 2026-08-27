package middleware

import (
	"net/http"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// RealIPConfig configures [RealIPConfig.Middleware].
type RealIPConfig struct {
	// Skip passes a request straight to the next handler when it returns true.
	Skip func(c router.Context) bool

	// Headers are the headers to read, in order of preference. They default to
	// X-Real-Ip and then X-Forwarded-For. A header that holds a comma
	// separated list contributes its first entry.
	Headers []string
}

// RealIP fills in the defaults of the config and returns it.
func RealIP(cfg RealIPConfig) RealIPConfig {
	if len(cfg.Headers) == 0 {
		cfg.Headers = []string{router.HeaderXRealIP, router.HeaderXForwardedFor}
	}
	return cfg
}

// Middleware replaces the remote address of the request with the address that
// a forwarding header reports.
//
// Use it only when a trusted proxy sits in front of the server and rewrites
// those headers. A client that reaches the server directly can set any of them
// to any value, so trusting them would let it forge its own address.
func (cfg RealIPConfig) Middleware[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	cfg = RealIP(cfg)
	return func(c C) error {
		if skipped(cfg.Skip, c) {
			return next(c)
		}
		req := c.Request()
		if ip := firstAddress(req.Header, cfg.Headers); ip != "" {
			// A shallow copy is enough, because only RemoteAddr changes.
			r := new(http.Request)
			*r = *req
			r.RemoteAddr = ip
			c.SetRequest(r)
		}
		return next(c)
	}
}

// firstAddress returns the first address that any of the headers reports.
func firstAddress(h http.Header, headers []string) string {
	for _, name := range headers {
		v := h.Get(name)
		if v == "" {
			continue
		}
		first, _, _ := strings.Cut(v, ",")
		if first = strings.TrimSpace(first); first != "" {
			return first
		}
	}
	return ""
}
