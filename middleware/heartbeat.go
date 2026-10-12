package middleware

import (
	"net/http"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// Heartbeat answers a GET or HEAD request for exactly path with a 200 and the
// body ".", as text/plain, and does not call the rest of the chain. It suits a
// liveness probe that must not touch auth, rate limits or the database.
//
// Middleware of the router runs after the route is chosen, and a route that
// does not exist is answered by the 404 chain of its scope. Mount Heartbeat
// with [router.Router.Use] on the root router, so that chain runs it too and
// path needs no route. Put it first, in front of [RealIP], [Logger] and the
// rest, to keep probes out of the logs and the limits.
//
// Heartbeat panics if path does not start with a slash.
func Heartbeat[C router.Context](path string) router.Middleware[C] {
	if !strings.HasPrefix(path, "/") {
		panic("middleware: Heartbeat needs a path that starts with a slash, as in \"/ping\"")
	}
	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			req := c.Request()
			if (req.Method == http.MethodGet || req.Method == http.MethodHead) && req.URL.Path == path {
				res := c.Response()
				res.Header().Set(router.HeaderContentType, "text/plain")
				res.WriteHeader(http.StatusOK)
				if req.Method == http.MethodGet {
					_, _ = res.Write([]byte(".")) // A probe that hung up has no one to tell.
				}
				return nil
			}
			return next(c)
		}
	}
}
