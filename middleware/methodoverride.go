package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// methodOverrideScan is how much of a urlencoded body [MethodOverride] reads
// to look for the _method field.
const methodOverrideScan = 64 << 10

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
// Reading the field looks at the first 64 KiB of a urlencoded body and never
// parses the form, so the router reads the whole body under its own body limit,
// or the one of the route. Put the _method field before the large ones: a field
// past that prefix is not seen. The handler still reads the full body.
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
		if method == "" && strings.HasPrefix(req.Header.Get("Content-Type"), "application/x-www-form-urlencoded") &&
			req.Body != nil && req.Body != http.NoBody {
			var prefix []byte
			method, prefix = scanMethodField(req.Body)
			out := req.WithContext(req.Context())
			out.Body = &prefixedBody{Reader: io.MultiReader(bytes.NewReader(prefix), req.Body), Closer: req.Body}
			req = out
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

// scanMethodField reads at most [methodOverrideScan] bytes of body and returns
// the _method field among the complete pairs in them, with the bytes it read.
func scanMethodField(body io.Reader) (method string, prefix []byte) {
	prefix, _ = io.ReadAll(io.LimitReader(body, methodOverrideScan)) // An error shows again when the router reads on.
	complete := prefix
	if len(prefix) == methodOverrideScan {
		// The last pair may be cut short.
		complete = prefix[:max(bytes.LastIndexByte(prefix, '&'), 0)]
	}
	values, _ := url.ParseQuery(string(complete)) // Pairs that parse still count.
	return values.Get(MethodOverrideField), prefix
}

// prefixedBody is a request body whose first bytes were read ahead.
type prefixedBody struct {
	io.Reader
	io.Closer
}
