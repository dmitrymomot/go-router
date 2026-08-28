// Package middleware holds the middleware that ships with the router.
//
// Every middleware comes in two forms. The plain one is a [router.Middleware]
// itself, with the default config, so it goes into Use without a call. The
// WithConfig one is a factory that takes a config:
//
//	r.Use(middleware.Recover[*app.Context])
//	r.Use(middleware.TimeoutWithConfig[*app.Context](middleware.TimeoutConfig{
//		Duration: 5 * time.Second,
//	}))
//
// The context type has to be written at the call site: Go infers a type
// argument from the arguments of a call, and these calls carry nothing that
// names the context. A type alias takes the repetition out of it:
//
//	type Ctx = *app.Context
//
//	r.Use(middleware.Recover[Ctx], middleware.RequestID[Ctx], middleware.RealIP[Ctx])
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
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// statusOf returns the status that the client sees, whether the handler wrote
// it or an error will produce it.
func statusOf[C router.Context](c C, err error) int {
	return router.ResolveStatus(c.Response(), err)
}

// skipped reports whether the config asks to pass this request through.
func skipped[C router.Context](skip func(router.Context) bool, c C) bool {
	return skip != nil && skip(c)
}

// originOf returns s as a bare origin, a scheme and a host and nothing else,
// and reports whether it is one. The origin comes back lowercased, which is
// the form that a comparison against the Origin header reads.
//
// A browser sends that header as a scheme and a host, and every comparison
// against it here is exact, so an entry that carries a path, or that leaves
// the scheme out, or that puts a wildcard in the host, matches no request that
// ever arrives. [CORSWithConfig] and [CSRFWithConfig] both refuse one at
// construction rather than let it fail as a blocked request in production.
func originOf(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Opaque != "" || u.Host == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		strings.Contains(u.Host, "*") {
		return "", false
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), true
}

// tooLarge maps an [http.MaxBytesError] that reached a middleware from a
// handler onto [router.ErrPayloadTooLarge], so that errors.Is finds the same
// error whether Bind hit the cap or the handler read past it itself.
//
// An error that already names a status keeps it: Bind writes its own 413, and
// a handler that turned the read into a failure of its own has said what the
// client reads. Every other error passes through untouched.
func tooLarge(err error, message string, limit int64) error {
	if _, ok := errors.AsType[*http.MaxBytesError](err); !ok {
		return err
	}
	if _, named := errors.AsType[*router.HTTPError](err); named {
		return err
	}
	return router.ErrPayloadTooLarge.WithMessage(message, limit).WithError(err)
}
