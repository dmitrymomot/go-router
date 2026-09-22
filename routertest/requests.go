package routertest

import (
	"iter"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// anyMethod is what Route.Method reports for a route of Router.Any and for a
// mount.
const anyMethod = "*"

// uuidValue is the value a {name:uuid} parameter gets when fill gives none.
const uuidValue = "123e4567-e89b-42d3-a456-426614174000"

// Requests yields one request for each route, with every named host and path
// parameter filled in, such as for a test that every route asks for a key.
//
// fill gives the value of the parameter param of rt. When fill is nil or gives
// "", a parameter of the class int gets "1", one of the class uuid gets a
// canonical UUID, and any other gets "x". An anonymous * label of a host gets
// "x" and never reaches fill. The path is built with [router.Expand], which
// escapes the values; a {name...} keeps its slashes. Host values go in as they
// are.
//
// A route of Router.Any or a mount goes out as a GET. The host of the route
// goes in before opts, so opts can override it.
//
// A value that its parameter refuses, such as "x" for a regular expression or
// a class that the router declares, or one that a static sibling claims, such
// as "new" next to /users/new, reaches another route or a 404. Give fill a
// value for it.
func Requests(
	routes []router.Route,
	fill func(rt router.Route, param string) string,
	opts ...RequestOption,
) iter.Seq2[router.Route, *http.Request] {
	opts = slices.Clone(opts)
	return func(yield func(router.Route, *http.Request) bool) {
		for _, rt := range routes {
			value := func(name, class string) string {
				if fill != nil {
					if v := fill(rt, name); v != "" {
						return v
					}
				}
				switch class {
				case "int":
					return "1"
				case "uuid":
					return uuidValue
				}
				return "x"
			}
			var reqOpts []RequestOption
			if rt.Host != "" {
				host, _ := fillPattern(rt.Host, true, value)
				reqOpts = append(reqOpts, Host(host))
			}
			path, pairs := fillPattern(rt.Pattern, false, value)
			if expanded, err := router.Expand(rt.Pattern, pairs...); err == nil {
				path = expanded
			}
			method := rt.Method
			if method == anyMethod {
				method = http.MethodGet
			}
			if !yield(rt, Request(method, path, append(reqOpts, opts...)...)) {
				return
			}
		}
	}
}

// fillPattern fills every parameter of a path pattern, or of a host pattern
// when host is set, with value. It follows the brace syntax of the router: a
// {name}, {name:constraint} or {name...} group, and a bare * that is the
// parameter "*" of a path or an anonymous label of a host. It reports the
// filled pattern, with path values escaped, and the name and value pairs that
// Expand takes. An unbalanced brace is copied as it is.
func fillPattern(pattern string, host bool, value func(name, class string) string) (string, []string) {
	sep := byte('/')
	if host {
		sep = '.'
	}
	var (
		b     strings.Builder
		pairs []string
	)
	for i, part := range splitOutsideBraces(pattern, sep) {
		if i > 0 {
			b.WriteByte(sep)
		}
		if part == "*" {
			if host {
				b.WriteString("x")
				continue
			}
			v := value(part, "")
			pairs = append(pairs, part, v)
			b.WriteString(escapeRest(v))
			continue
		}
		for part != "" {
			open := strings.IndexByte(part, '{')
			if open < 0 {
				b.WriteString(part)
				break
			}
			end := closingBrace(part, open)
			if end < 0 {
				b.WriteString(part)
				break
			}
			b.WriteString(part[:open])
			name, class, found := strings.Cut(part[open+1:end], ":")
			rest := false
			if !found {
				name, rest = strings.CutSuffix(name, "...")
			}
			v := value(name, class)
			pairs = append(pairs, name, v)
			switch {
			case host:
				b.WriteString(v)
			case rest:
				b.WriteString(escapeRest(v))
			default:
				b.WriteString(url.PathEscape(v))
			}
			part = part[end+1:]
		}
	}
	return b.String(), pairs
}

// splitOutsideBraces cuts s at every sep that no brace group holds.
func splitOutsideBraces(s string, sep byte) []string {
	var (
		parts []string
		start int
		depth int
	)
	for i := range len(s) {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		case sep:
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

// closingBrace reports the index of the brace that closes the one at open, or
// -1.
func closingBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// escapeRest path-escapes each piece of a {name...} value and keeps the
// slashes between them.
func escapeRest(v string) string {
	pieces := strings.Split(v, "/")
	for i, p := range pieces {
		pieces[i] = url.PathEscape(p)
	}
	return strings.Join(pieces, "/")
}
