// Package profiler serves the pprof pages and the expvar variables of the
// process.
//
// It lives apart from package middleware because importing net/http/pprof and
// expvar registers their handlers on [http.DefaultServeMux]. Only a program
// that imports profiler pays for that.
package profiler

import (
	"expvar"
	"net/http"
	"net/http/pprof"

	"github.com/dmitrymomot/go-router"
)

// Handler returns a handler that serves the pprof pages and the expvar
// variables:
//
//	/pprof/    the index of the profiles
//	/pprof/cmdline, /pprof/profile, /pprof/symbol, /pprof/trace
//	/vars      the expvar variables, as JSON
//
// Mount it with router.Router.MountHandler, as in
// r.MountHandler("/debug", profiler.Handler()), which gives /debug/pprof/
// and /debug/vars. The index page finds the profiles by that /debug prefix, so
// mount it there.
//
// The pages expose memory, code paths and command lines, and a profile costs
// CPU, so never leave Handler open: mount it behind middleware.BasicAuth or
// middleware.KeyAuth, or on a listener that is not public.
//
// Importing net/http/pprof and expvar also registers their handlers on
// [http.DefaultServeMux], which a router does not use. If the program serves
// that mux anywhere, those pages are open there; serve the router instead.
func Handler() http.Handler {
	r := router.New(func(http.ResponseWriter, *http.Request) *profilerContext { return new(profilerContext) })
	r.GET("/pprof", func(c *profilerContext) error {
		// A relative Location, which the client resolves against the URL it asked
		// for, with the prefix of the mount.
		return c.Redirect(http.StatusMovedPermanently, "pprof/")
	})
	// Index serves the index and every named profile. It reads the name after
	// "/debug/pprof/" in the path, which the mount has cut down, so put that
	// prefix back.
	r.GET("/pprof/{name...}", func(c *profilerContext) error {
		req := c.Request()
		u := *req.URL
		u.Path = "/debug/pprof/" + c.Param("name")
		u.RawPath = ""
		out := req.Clone(req.Context())
		out.URL = &u
		pprof.Index(c.Response(), out)
		return nil
	})
	r.GET("/pprof/cmdline", router.WrapHandlerFunc[*profilerContext](pprof.Cmdline))
	r.GET("/pprof/profile", router.WrapHandlerFunc[*profilerContext](pprof.Profile))
	r.GET("/pprof/trace", router.WrapHandlerFunc[*profilerContext](pprof.Trace))
	// The pprof tool posts the addresses it looks up.
	r.Match([]string{http.MethodGet, http.MethodPost}, "/pprof/symbol", router.WrapHandlerFunc[*profilerContext](pprof.Symbol))
	r.GET("/vars", router.WrapHandler[*profilerContext](expvar.Handler()))
	return r
}

// profilerContext is the context of the profiler's own router.
type profilerContext struct{ router.Base }
