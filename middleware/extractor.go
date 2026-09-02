package middleware

import (
	"fmt"
	"iter"
	"net/textproto"
	"slices"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// MaxTokenSources is how many [TokenSource] values one configuration may
// declare.
const MaxTokenSources = 20

// MaxTokensPerRequest is how many candidate tokens one request may offer
// across every source. It bounds the work a client can ask for by repeating a
// header.
const MaxTokensPerRequest = 8

// TokenSource reads the candidate tokens of one request out of one place. Each
// value it reports is tried in turn, so a header the client repeats offers
// several. See [FromHeader], [FromQuery], [FromCookie] and [FromForm].
type TokenSource func(c router.Context) []string

// FromHeader reads a header. cutPrefix is a scheme to strip, as in
// FromHeader("Authorization", "Bearer "), and the space after it is trimmed; an
// empty cutPrefix takes the whole value. A value that does not carry the prefix
// is dropped.
func FromHeader(name, cutPrefix string) TokenSource {
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
				// RFC 6750 allows more than one space after the scheme, and
				// RFC 9110 allows whitespace besides.
				out = append(out, strings.TrimLeft(v[n:], " \t"))
			}
		}
		return nonEmpty(out)
	}
}

// FromQuery reads a query parameter. It suits a link a browser cannot send a
// header on, and it puts the token in the logs of every proxy on the way.
func FromQuery(name string) TokenSource {
	return func(c router.Context) []string {
		b, ok := router.FromContext(c)
		if !ok {
			return nil
		}
		return nonEmpty(b.QueryValues()[name])
	}
}

// FromCookie reads a cookie.
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

// FromForm reads a field of the form body, which is how a plain HTML form
// sends its CSRF token. It parses the body once per request, so the handler
// can still read the form.
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

func nonEmpty(values []string) []string {
	if !slices.Contains(values, "") {
		return values
	}
	return slices.DeleteFunc(slices.Clone(values), func(v string) bool { return v == "" })
}

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
