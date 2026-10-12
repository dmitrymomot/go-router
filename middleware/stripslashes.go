package middleware

import (
	"net/http"
	"strings"
)

// StripSlashes removes the trailing slashes from the path of a request, except
// for the path "/" itself, so /users/ reaches the route /users.
//
// Middleware of the router runs after the route is chosen, so a path that
// middleware rewrites cannot pick another route. StripSlashes is therefore a
// plain [http.Handler] wrapper that goes in front of the router:
//
//	http.ListenAndServe(addr, middleware.StripSlashes(r))
//
// The request the next handler gets is a copy; the original is not changed.
func StripSlashes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		path := trimSlashes(req.URL.Path)
		if path == req.URL.Path {
			next.ServeHTTP(w, req)
			return
		}
		u := *req.URL
		u.Path = path
		if u.RawPath != "" {
			u.RawPath = trimSlashes(u.RawPath)
		}
		out := req.WithContext(req.Context())
		out.URL = &u
		next.ServeHTTP(w, out)
	})
}

func trimSlashes(p string) string {
	if t := strings.TrimRight(p, "/"); t != "" {
		return t
	}
	if p == "" {
		return ""
	}
	return "/"
}
