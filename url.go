package router

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// namedRoute is the pattern that one name resolves to, ready to fill in.
type namedRoute struct {
	// pattern is the compiled pattern of the route, which the error messages
	// quote and a duplicate name compares against.
	pattern string

	// parts is that pattern cut into literal text and parameters, so that
	// building a path costs no parsing.
	parts []urlPart
}

// urlPart is one piece of a named pattern: literal text to copy, or a
// parameter to fill in.
type urlPart struct {
	// lit is the text to copy. It is empty for a parameter.
	lit string

	// name is the parameter to read, empty for literal text.
	name string

	// rest reports a "{name...}" or a "*" parameter, whose value spans the end
	// of the path and keeps its separators.
	rest bool
}

// URL builds the path of a named route from its parameters:
//
//	r.Name("post").GET("/blog/{year}/{slug}", showPost)
//	r.URL("post", map[string]string{"year": "2026", "slug": "hello"})   // "/blog/2026/hello"
//
// It reports an error for a name that no route carries, for a parameter that
// the pattern declares and params leaves out, and for one that params holds
// and the pattern does not. Each value is percent-encoded; a "{rest...}" value
// keeps its separators, and every segment between them is encoded.
//
// The result is a path, not an absolute URL. A named route inside a host scope
// resolves to its path alone, because a host pattern carries parameters of its
// own that the route knows nothing about.
//
// The first call compiles the trie, the way [Router.Routes] does, so it has to
// come after the last route is registered.
func (r *Router[C]) URL(name string, params map[string]string) (string, error) {
	root := r.root
	if err := root.compile(); err != nil {
		return "", err
	}
	nr, ok := root.named[name]
	if !ok {
		return "", fmt.Errorf("router: no route is named %q", name)
	}
	return expandURL(name, nr, params)
}

// MustURL builds the path of a named route from alternating keys and values,
// and panics when [Router.URL] would report an error:
//
//	r.MustURL("post", "year", "2026", "slug", "hello")   // "/blog/2026/hello"
//
// The router compiles its routes once, so an unknown name, a missing parameter
// and a spare one are all mistakes in the code and not in the request. Reach
// for it where a link is built from constants, and for [Router.URL] where a
// name or a value comes from data.
func (r *Router[C]) MustURL(name string, kv ...string) string {
	if len(kv)%2 != 0 {
		panic(fmt.Sprintf("router: MustURL(%q) needs alternating keys and values, but got %d arguments", name, len(kv)))
	}
	var params map[string]string
	if len(kv) > 0 {
		params = make(map[string]string, len(kv)/2)
		for i := 0; i < len(kv); i += 2 {
			params[kv[i]] = kv[i+1]
		}
	}
	out, err := r.URL(name, params)
	if err != nil {
		panic(err.Error())
	}
	return out
}

// expandURL fills the parameters of a pattern in.
func expandURL(name string, nr namedRoute, params map[string]string) (string, error) {
	var (
		b        strings.Builder
		used     int
		trimTail bool
	)
	b.Grow(len(nr.pattern))
	for _, p := range nr.parts {
		if p.name == "" {
			b.WriteString(p.lit)
			continue
		}
		v, ok := params[p.name]
		if !ok {
			return "", fmt.Errorf("router: the route %q needs the parameter %q of %q", name, p.name, nr.pattern)
		}
		used++
		switch {
		case p.rest && v == "":
			// A catch-all also matches an empty remainder, the way /files
			// answers /files/{path...}, so the separator in front of it goes
			// with the value.
			trimTail = true
		case p.rest:
			// The separators of the value are the ones that the pattern
			// promised, so they stay and only the text between them is
			// encoded.
			b.WriteString(escapeRest(v))
		case v == "":
			return "", fmt.Errorf("router: the parameter %q of the route %q is empty, and %q matches no path with an empty segment", p.name, name, nr.pattern)
		default:
			b.WriteString(url.PathEscape(v))
		}
	}
	if used != len(params) {
		return "", fmt.Errorf("router: the route %q takes no parameter %s in %q", name, quoteList(spareParams(nr.parts, params)), nr.pattern)
	}
	out := b.String()
	if trimTail {
		if out = strings.TrimSuffix(out, "/"); out == "" {
			out = "/"
		}
	}
	return out, nil
}

// escapeRest encodes the value of a catch-all. The separators are the ones
// that the pattern promised, so they stay, and each segment between them is
// encoded like any other value.
func escapeRest(v string) string {
	if !strings.Contains(v, "/") {
		return url.PathEscape(v)
	}
	var b strings.Builder
	b.Grow(len(v))
	sep := ""
	for seg := range strings.SplitSeq(v, "/") {
		b.WriteString(sep)
		b.WriteString(url.PathEscape(seg))
		sep = "/"
	}
	return b.String()
}

// spareParams returns the keys of params that the pattern does not declare, in
// a stable order.
func spareParams(parts []urlPart, params map[string]string) []string {
	var out []string
	for k := range params {
		if !slices.ContainsFunc(parts, func(p urlPart) bool { return p.name == k }) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// quoteList renders names for an error message.
func quoteList(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = `"` + n + `"`
	}
	return strings.Join(out, ", ")
}

// parseURLTemplate cuts a compiled pattern into the text to copy and the
// parameters to fill in. The pattern reached the trie first, so it is
// well-formed here and the walk needs no error path.
func parseURLTemplate(pattern string) []urlPart {
	var (
		parts []urlPart
		lit   int
	)
	for i := 0; i < len(pattern); {
		switch pattern[i] {
		case '{':
			end, ok := closingBrace(pattern, i)
			if !ok {
				return append(parts, urlPart{lit: pattern[lit:]})
			}
			if lit < i {
				parts = append(parts, urlPart{lit: pattern[lit:i]})
			}
			body := pattern[i+1 : end]
			name, _, _ := strings.Cut(body, ":")
			name, rest := strings.CutSuffix(name, "...")
			parts = append(parts, urlPart{name: name, rest: rest})
			i, lit = end+1, end+1

		case '*':
			// An anonymous catch-all, which spans a whole segment and carries
			// the name that Param answers to.
			if lit < i {
				parts = append(parts, urlPart{lit: pattern[lit:i]})
			}
			parts = append(parts, urlPart{name: mountParam, rest: true})
			i, lit = i+1, i+1

		default:
			i++
		}
	}
	if lit < len(pattern) {
		parts = append(parts, urlPart{lit: pattern[lit:]})
	}
	return parts
}
