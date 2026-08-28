package middleware

import (
	"fmt"
	"iter"
	"net/textproto"
	"slices"
	"strings"

	"github.com/dmitrymomot/go-router"
)

const MaxTokenSources = 20

const MaxTokensPerRequest = 8

type TokenSource func(c router.Context) []string

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
				out = append(out, v[n:])
			}
		}
		return out
	}
}

func FromQuery(name string) TokenSource {
	return func(c router.Context) []string {
		b, ok := router.FromContext(c)
		if !ok {
			return nil
		}
		return nonEmpty(b.QueryValues()[name])
	}
}

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
