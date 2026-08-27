package middleware

import (
	"net/http"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// RealIP replaces the remote address of the request with the address that
// X-Real-Ip, or the first entry of X-Forwarded-For, reports.
//
// Use it only when a trusted proxy sits in front of the server and rewrites
// those headers. A client that reaches the server directly can set either
// header to any value, so trusting them would let it forge its own address.
func RealIP[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return func(c C) error {
		req := c.Request()
		if ip := realIP(req.Header.Get(router.HeaderXRealIP), req.Header.Get(router.HeaderXForwardedFor)); ip != "" {
			// A shallow copy is enough, because only RemoteAddr changes.
			r := new(http.Request)
			*r = *req
			r.RemoteAddr = ip
			c.SetRequest(r)
		}
		return next(c)
	}
}

// realIP picks the first usable address from the two headers.
func realIP(realIP, forwardedFor string) string {
	if realIP != "" {
		return strings.TrimSpace(realIP)
	}
	first, _, _ := strings.Cut(forwardedFor, ",")
	return strings.TrimSpace(first)
}
