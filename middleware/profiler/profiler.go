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
	mux := http.NewServeMux()
	mux.HandleFunc("/pprof", func(w http.ResponseWriter, req *http.Request) {
		// A relative Location, which the client resolves against the URL it asked
		// for, with the prefix of the mount.
		w.Header().Set("Location", "pprof/")
		w.WriteHeader(http.StatusMovedPermanently)
	})
	// Index looks for the profile name after "/debug/pprof/" in the path, which
	// MountHandler has cut down, so put that prefix back.
	mux.HandleFunc("/pprof/{rest...}", func(w http.ResponseWriter, req *http.Request) {
		out := req.WithContext(req.Context())
		u := *req.URL
		u.Path = "/debug/pprof/" + req.PathValue("rest")
		u.RawPath = ""
		out.URL = &u
		pprof.Index(w, out)
	})
	mux.HandleFunc("/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/pprof/profile", pprof.Profile)
	mux.HandleFunc("/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/pprof/trace", pprof.Trace)
	mux.Handle("/vars", expvar.Handler())
	return mux
}
