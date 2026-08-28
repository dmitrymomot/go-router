package middleware

import (
	"fmt"
	"iter"
	"net/textproto"
	"slices"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// MaxTokenSources is the number of [TokenSource] values that one config takes.
// A config that declares more panics, because a request that walks an
// unbounded list of sources spends the time of the server on nothing.
const MaxTokenSources = 20

// MaxTokensPerRequest is the number of credentials that one request presents.
// A header repeats and so does a form field, so a single request would
// otherwise hand a validator any number of keys to check.
const MaxTokensPerRequest = 8

// TokenSource reads credentials out of a request. It returns every one that it
// finds, in the order in which the request carries them, and never an empty
// value.
//
// It is a function and not a string such as "header:X-Api-Key", so the
// compiler reads the source at the call site and a typo in it never reaches a
// running server:
//
//	middleware.KeyAuthWithConfig[Ctx](middleware.KeyAuthConfig[Ctx]{
//		Sources:   []middleware.TokenSource{middleware.FromHeader("X-Api-Key", "")},
//		Validator: keys.Check,
//	})
//
// Write one of your own for a place that the four here do not reach.
type TokenSource func(c router.Context) []string

// FromHeader reads a request header. cutPrefix is the authentication scheme
// that the value carries, "Bearer " for an OAuth 2 token, and the source cuts
// it off. The comparison ignores case, because RFC 7235 lets a client spell
// the scheme any way it likes. Pass an empty prefix for a header that holds
// the credential alone.
//
// A value that does not carry the prefix belongs to another scheme, so the
// source passes over it. A header that repeats contributes each of its values.
func FromHeader(name, cutPrefix string) TokenSource {
	// The map behind http.Header is keyed by the canonical form, which costs
	// nothing to resolve once here and a scan of the name on every request
	// through Get.
	key := textproto.CanonicalMIMEHeaderKey(name)
	n := len(cutPrefix)

	return func(c router.Context) []string {
		values := c.Request().Header[key]
		if n == 0 {
			return nonEmpty(values)
		}
		var out []string
		for _, v := range values {
			if len(v) > n && strings.EqualFold(v[:n], cutPrefix) {
				out = append(out, v[n:])
			}
		}
		return out
	}
}

// FromQuery reads a query parameter. Reach for it only where a header cannot
// go, such as the URL of a download that the browser follows on its own,
// because a query string reaches the access log of every proxy on the way and
// the Referer header of the next page.
func FromQuery(name string) TokenSource {
	return func(c router.Context) []string {
		b, ok := router.FromContext(c)
		if !ok {
			return nil
		}
		return nonEmpty(b.QueryValues()[name])
	}
}

// FromCookie reads a cookie. A request that carries the cookie twice
// contributes both values.
func FromCookie(name string) TokenSource {
	return func(c router.Context) []string {
		cookies := c.Request().CookiesNamed(name)
		out := make([]string, 0, len(cookies))
		for _, cookie := range cookies {
			if cookie.Value != "" {
				out = append(out, cookie.Value)
			}
		}
		return out
	}
}

// FromForm reads a field of the request body, which is where a browser posts a
// hidden token.
//
// It parses the body through the context, so the parse obeys
// [router.Router.MaxBodyBytes] and a multipart part spills to disk past
// [router.Router.MaxMultipartMemory]. It leaves the body readable: net/http
// parses a request once and answers every later reader from the form it parsed
// then, so the handler still reads the same fields and the same uploads.
//
// It reads the body alone and never the query string, so a parameter in the
// URL cannot forge the credential. A body that does not parse yields nothing,
// and the middleware reports the credential that is missing rather than the
// parse that failed.
//
// A urlencoded body reaches it on POST, PUT and PATCH alone, because those are
// the methods for which [net/http.Request.ParseForm] reads a body at all: a
// DELETE that spells the credential in application/x-www-form-urlencoded hands
// over nothing, and so does [router.Base.FormValue] in the handler after it.
// This bites the request that a method override turned into a DELETE, so put
// the credential in a header or in a multipart body there, which parses on any
// method.
//
// It does not fit a handler that reads the body itself through
// [net/http.Request.MultipartReader], because that handler needs a body that
// nothing has consumed. Carry the credential in a header there.
func FromForm(name string) TokenSource {
	return func(c router.Context) []string {
		b, ok := router.FromContext(c)
		if !ok {
			return nil
		}
		vals, err := b.FormValues()
		if err != nil {
			return nil
		}
		return nonEmpty(vals[name])
	}
}

// nonEmpty returns the values that hold something, so that a validator never
// runs on the empty string that "?token=" spells.
func nonEmpty(values []string) []string {
	if !slices.Contains(values, "") {
		return values
	}
	return slices.DeleteFunc(slices.Clone(values), func(v string) bool { return v == "" })
}

// checkTokenSources panics on a config that declares more than
// [MaxTokenSources] sources, and on one that declares a nil source. Both are
// faults of the wiring, and both would otherwise surface as a failure of a
// request that reaches production.
func checkTokenSources(config string, sources []TokenSource) {
	if len(sources) > MaxTokenSources {
		panic(fmt.Sprintf("middleware: %s declares %d token sources, at most %d",
			config, len(sources), MaxTokenSources))
	}
	for i, src := range sources {
		if src == nil {
			panic(fmt.Sprintf("middleware: %s declares a nil token source at index %d", config, i))
		}
	}
}

// readTokens iterates the credentials that the sources yield, at most
// [MaxTokensPerRequest] of them. The bound is what caps the work that one
// request asks of a validator.
func readTokens(c router.Context, sources []TokenSource) iter.Seq[string] {
	return func(yield func(string) bool) {
		n := 0
		for _, src := range sources {
			for _, token := range src(c) {
				if n++; n > MaxTokensPerRequest {
					return
				}
				if !yield(token) {
					return
				}
			}
		}
	}
}
