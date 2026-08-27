// Package middleware holds the middleware that ships with the router.
//
// Every middleware comes as two factories. The plain one returns the
// middleware with its default config, and the WithConfig one takes a config:
//
//	r.Use(middleware.Recover[*app.Context]())
//	r.Use(middleware.TimeoutWithConfig[*app.Context](middleware.TimeoutConfig{
//		Duration: 5 * time.Second,
//	}))
//
// A factory returns [router.Middleware] directly, so the context type has to
// be written at the call site: Go infers a type argument from the arguments of
// a call, and these calls carry nothing that names the context. A type alias
// takes the repetition out of it:
//
//	type Ctx = *app.Context
//
//	r.Use(middleware.Recover[Ctx](), middleware.RequestID[Ctx](), middleware.RealIP[Ctx]())
//
// Every config carries a Skip function. Return true from it to pass the
// request straight to the next handler:
//
//	middleware.LoggerWithConfig[Ctx](middleware.LoggerConfig{
//		Skip: func(c router.Context) bool { return c.Request().URL.Path == "/health" },
//	})
//
// Skip takes the [router.Context] interface, not the application context type,
// so one function fits any router. Use [router.Context.Request] for the
// request and [router.Context.RoutePattern] for the matched route.
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

// skipped reports whether the config asks to pass this request through.
func skipped[C router.Context](skip func(router.Context) bool, c C) bool {
	return skip != nil && skip(c)
}
