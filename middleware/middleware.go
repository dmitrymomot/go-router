// Package middleware holds the middleware that ships with the router.
//
// Every middleware follows the same shape. A function named after it takes an
// optional config value, fills in the defaults and returns the config. That
// config carries a generic Middleware method, and Go 1.27 infers the context
// type there, so a caller never names its own context type:
//
//	r.Use(middleware.Recover().Middleware)
//	r.Use(middleware.Timeout(middleware.TimeoutConfig{Duration: 5 * time.Second}).Middleware)
//
// Leave the config out to take the defaults. Passing more than one panics,
// because the middleware would have to guess which one wins.
//
// Every config carries a Skip function. Return true from it to pass the
// request straight to the next handler:
//
//	middleware.LoggerConfig{
//		Skip: func(c router.Context) bool { return c.Request().URL.Path == "/health" },
//	}
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

// only returns the single config of a variadic argument, or the zero value
// when the caller passed none.
func only[T any](name string, cfgs []T) T {
	switch len(cfgs) {
	case 0:
		var zero T
		return zero
	case 1:
		return cfgs[0]
	default:
		panic("middleware: " + name + " takes at most one config")
	}
}

// skipped reports whether the config asks to pass this request through.
func skipped[C router.Context](skip func(router.Context) bool, c C) bool {
	return skip != nil && skip(c)
}
