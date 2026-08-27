// Package middleware holds the middleware that ships with the router.
//
// A middleware without settings is a plain generic function, so type inference
// resolves the context type at the point of use:
//
//	r.Use(middleware.Recover, middleware.RealIP, middleware.RequestID)
//
// A middleware with settings is a value with a generic Middleware method, new
// in Go 1.27, and inference resolves the context type there as well:
//
//	r.Use(middleware.Logger(log).Middleware)
//	r.Use(middleware.CORS(middleware.CORSConfig{AllowOrigins: []string{"*"}}).Middleware)
package middleware

import (
	"net"

	"github.com/dmitrymomot/go-router"
)

// ClientIP returns the address of the client, without the port. Put [RealIP]
// in front of it to read the address that a trusted proxy reports.
func ClientIP[C router.Context](c C) string {
	host, _, err := net.SplitHostPort(c.Request().RemoteAddr)
	if err != nil {
		return c.Request().RemoteAddr
	}
	return host
}

// statusOf returns the status that the client sees, whether the handler wrote
// it or an error will produce it.
func statusOf[C router.Context](c C, err error) int {
	if st := c.Response().Status; st != 0 {
		return st
	}
	return router.StatusOf(err)
}
