package middleware

import (
	"net/http"
	"strings"
)

// HeaderMethodOverride is the header [MethodOverride] reads.
const HeaderMethodOverride = "X-HTTP-Method-Override"

// MethodOverrideField is the form field [MethodOverride] reads.
const MethodOverrideField = "_method"

// MethodOverride lets a client that can only send GET and POST, such as an
// HTML form, reach the PUT, PATCH and DELETE routes. A POST takes its method
// from the X-HTTP-Method-Override header, or else from the _method field of a
// urlencoded form. Only PUT, PATCH and DELETE count, in any letter case; any
// other value is ignored and the request stays a POST. A request that is not
// a POST is never changed.
//
// Reading the field parses the form, which reads the body of a urlencoded POST
// under the limit of [http.Request.ParseForm]. The form stays available to the
// handler.
//
// Middleware of the router runs after the route is chosen, so MethodOverride
// is a plain [http.Handler] wrapper that goes in front of the router:
//
//	http.ListenAndServe(addr, middleware.MethodOverride(r))
//
// The request the next handler gets is a copy; the original is not changed.
func MethodOverride(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			next.ServeHTTP(w, req)
			return
		}
		method := req.Header.Get(HeaderMethodOverride)
		if method == "" && strings.HasPrefix(req.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			_ = req.ParseForm() // A form that does not parse has no field; the handler meets the same error.
			method = req.PostForm.Get(MethodOverrideField)
		}
		switch method = strings.ToUpper(method); method {
		case http.MethodPut, http.MethodPatch, http.MethodDelete:
			out := req.WithContext(req.Context())
			out.Method = method
			req = out
		}
		next.ServeHTTP(w, req)
	})
}
