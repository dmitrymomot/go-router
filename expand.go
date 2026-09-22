package router

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// Expand fills the parameters of a route or host pattern with values given as
// name and value pairs, and reports the path or host the pattern then matches:
// Expand("/agents/{agent}/suspend", "agent", id). A pattern that starts with a
// slash is a path: each value is path-escaped, a {name...} keeps its slashes and
// may be empty, the path comes out as the router matches it (no trailing slash),
// and a query after '?' may name parameters as bare {name}, whose values are
// query-escaped and may be empty. Any other pattern is a host, such as
// "{tenant}.example.com", whose values are written as they are and checked.
//
// Values are checked against regular expressions and the built-in classes int,
// slug and uuid. A class that a router declares with [Router.ParamClass] is
// unknown to Expand and admits any value.
//
// Expand reports an error for a pattern the router refuses, a pattern with a '/'
// that does not start with one, a wildcard host, a name the pattern lacks, a name
// given twice or not at all, an odd number of arguments, an empty value for a
// plain path segment or a host label, a path that would start with "//" (which a
// browser reads as another host) or hold a "." or ".." segment (which a
// browser resolves to another path), and a value that would not route back to the
// same parameters, such as "web-api" in "/r/{env}-{name}" or "Acme" in a host.
// The pattern is meant to be a constant shared with the registration.
func Expand(pattern string, pairs ...string) (string, error) {
	if len(pairs)%2 != 0 {
		return "", fmt.Errorf("router: Expand(%q) needs alternating names and values, but got %d arguments", pattern, len(pairs))
	}
	t, err := templateFor(pattern)
	if err != nil {
		return "", err
	}
	if err := t.checkPairs(pairs); err != nil {
		return "", err
	}
	return t.expand(func(name string) (string, bool) {
		for i := 0; i < len(pairs); i += 2 {
			if pairs[i] == name {
				return pairs[i+1], true
			}
		}
		return "", false
	})
}

// MustExpand is [Expand] for a pattern and values the program controls. It
// panics where Expand reports an error; use Expand for a value from a request.
func MustExpand(pattern string, pairs ...string) string {
	out, err := Expand(pattern, pairs...)
	if err != nil {
		panic(err.Error())
	}
	return out
}

// Past this many patterns Expand compiles on every call, so a program that
// builds patterns at run time cannot grow the cache without bound.
const maxCachedTemplates = 1024

var (
	templateCache  sync.Map // pattern -> *urlTemplate
	cachedPatterns atomic.Int64
)

func templateFor(pattern string) (*urlTemplate, error) {
	if t, ok := templateCache.Load(pattern); ok {
		return t.(*urlTemplate), nil
	}
	t, err := compileTemplate(pattern, anyClass)
	if err != nil {
		return nil, err
	}
	if cachedPatterns.Load() < maxCachedTemplates {
		if _, loaded := templateCache.LoadOrStore(pattern, t); !loaded {
			cachedPatterns.Add(1)
		}
	}
	return t, nil
}

type urlPart struct {
	lit  string
	name string
	// constraint is the text after the colon, such as "int" or "[0-9]+".
	constraint string
	rest       bool
}

// urlTemplate is a pattern cut into the parts that Expand writes. A path
// template keeps its segments to check that the path routes back; a host
// template keeps its spec for the same reason.
type urlTemplate struct {
	pattern  string
	path     []urlPart
	query    []urlPart
	names    []string
	segs     []segment
	host     *hostSpec
	hasQuery bool
	recheck  bool
}

func compileTemplate(pattern string, classes classLookup) (*urlTemplate, error) {
	if !strings.HasPrefix(pattern, "/") {
		if indexOutsideBraces(pattern, '/') >= 0 || indexOutsideBraces(pattern, '?') >= 0 {
			return nil, fmt.Errorf("router: a path pattern must start with \"/\", but %q does not", pattern)
		}
		return compileHostTemplate(pattern, classes)
	}

	path, query, hasQuery := pattern, "", false
	if i := indexOutsideBraces(pattern, '?'); i >= 0 {
		path, query, hasQuery = pattern[:i], pattern[i+1:], true
	}
	path = normalizePattern(path)
	segs, names, err := parsePattern(path, classes)
	if err != nil {
		return nil, err
	}
	t := &urlTemplate{
		pattern:  pattern,
		path:     parseURLTemplate(path),
		names:    names,
		segs:     segs,
		hasQuery: hasQuery,
		recheck:  needsRoundTrip(segs),
	}
	if hasQuery {
		if t.query, err = parseQueryTemplate(query, pattern); err != nil {
			return nil, err
		}
		for _, p := range t.query {
			if p.name != "" && !slices.Contains(t.names, p.name) {
				t.names = append(t.names, p.name)
			}
		}
	}
	return t, nil
}

func compileHostTemplate(pattern string, classes classLookup) (*urlTemplate, error) {
	spec, err := parseHostPattern(pattern, classes)
	if err != nil {
		return nil, err
	}
	if spec.any || slices.ContainsFunc(spec.labels, func(l hostLabel) bool { return l.parts == nil && l.lit == "" }) {
		return nil, fmt.Errorf("router: a wildcard host such as %q names no host to build", pattern)
	}
	return &urlTemplate{pattern: pattern, path: parseURLTemplate(spec.pattern), names: spec.names, host: &spec}, nil
}

// parseQueryTemplate cuts the query of a path pattern. It takes a bare {name}
// alone: a constraint or a catch-all means nothing in a query.
func parseQueryTemplate(query, pattern string) ([]urlPart, error) {
	var parts []urlPart
	lit := 0
	for i := 0; i < len(query); {
		switch query[i] {
		case '{':
			end, ok := closingBrace(query, i)
			if !ok {
				return nil, fmt.Errorf("router: unbalanced braces in the query of %q", pattern)
			}
			name := query[i+1 : end]
			if name == "" || strings.ContainsAny(name, ":{}") || strings.HasSuffix(name, "...") {
				return nil, fmt.Errorf("router: the query of %q may name a parameter only as a bare {name}, not {%s}", pattern, name)
			}
			if lit < i {
				parts = append(parts, urlPart{lit: query[lit:i]})
			}
			parts = append(parts, urlPart{name: name})
			i, lit = end+1, end+1
		case '}':
			return nil, fmt.Errorf("router: unbalanced braces in the query of %q", pattern)
		case '#', ' ':
			return nil, fmt.Errorf("router: the query of %q holds %q, which a URL must escape", pattern, string(query[i]))
		default:
			i++
		}
	}
	if lit < len(query) {
		parts = append(parts, urlPart{lit: query[lit:]})
	}
	return parts, nil
}

// checkPairs reports a name the template lacks, a name given twice, and the
// names left without a value.
func (t *urlTemplate) checkPairs(pairs []string) error {
	var spare []string
	for i := 0; i < len(pairs); i += 2 {
		name := pairs[i]
		if !slices.Contains(t.names, name) {
			spare = append(spare, name)
			continue
		}
		for j := 0; j < i; j += 2 {
			if pairs[j] == name {
				return fmt.Errorf("router: Expand(%q) got the parameter %q twice", t.pattern, name)
			}
		}
	}
	if len(spare) > 0 {
		slices.Sort(spare)
		return fmt.Errorf("router: %q has no parameter %s", t.pattern, quoteList(spare))
	}
	var missing []string
	for _, name := range t.names {
		found := false
		for i := 0; i < len(pairs); i += 2 {
			if pairs[i] == name {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("router: %q needs a value for %s", t.pattern, quoteList(missing))
	}
	return nil
}

// expand writes the template with the values that value reports. A name
// without a value counts as an empty one.
func (t *urlTemplate) expand(value func(name string) (string, bool)) (string, error) {
	if t.host != nil {
		return t.expandHost(value)
	}
	var (
		b        strings.Builder
		trimTail bool
	)
	b.Grow(len(t.pattern) + 16)
	for _, p := range t.path {
		if p.name == "" {
			b.WriteString(p.lit)
			continue
		}
		v, _ := value(p.name)
		switch {
		case p.rest && v == "":
			trimTail = true
		case p.rest:
			b.WriteString(escapeRest(v))
		case v == "":
			return "", fmt.Errorf("router: the parameter %q of %q is empty, and a path segment cannot be", p.name, t.pattern)
		default:
			b.WriteString(url.PathEscape(v))
		}
	}
	path := b.String()
	if trimTail {
		if path = strings.TrimSuffix(path, "/"); path == "" {
			path = "/"
		}
	}
	if strings.HasPrefix(path, "//") {
		return "", fmt.Errorf("router: %q builds %q, which a browser reads as a link to another host", t.pattern, path)
	}
	if hasDotSegment(path) {
		return "", fmt.Errorf("router: %q builds %q, whose dot segment a browser resolves to another path", t.pattern, path)
	}
	if t.recheck && !t.routesBack(path, value) {
		return "", fmt.Errorf("router: %q builds %q, which does not route back to the same values", t.pattern, path)
	}
	if !t.hasQuery {
		return path, nil
	}
	b.Reset()
	b.WriteString(path)
	b.WriteByte('?')
	for _, p := range t.query {
		if p.name == "" {
			b.WriteString(p.lit)
			continue
		}
		v, _ := value(p.name)
		b.WriteString(url.QueryEscape(v))
	}
	return b.String(), nil
}

func (t *urlTemplate) expandHost(value func(name string) (string, bool)) (string, error) {
	var b strings.Builder
	b.Grow(len(t.pattern) + 16)
	for _, p := range t.path {
		if p.name == "" {
			b.WriteString(p.lit)
			continue
		}
		v, _ := value(p.name)
		if v == "" {
			return "", fmt.Errorf("router: the parameter %q of %q is empty, and a host label cannot be", p.name, t.pattern)
		}
		if !p.rest && strings.IndexByte(v, '.') >= 0 {
			return "", fmt.Errorf("router: the parameter %q of %q is one label, so its value %q cannot hold a '.'", p.name, t.pattern, v)
		}
		b.WriteString(v)
	}
	host := b.String()
	bad := func() error {
		return fmt.Errorf("router: %q builds the host %q, which does not route back to the same values", t.pattern, host)
	}
	if norm, ok := normalizeHostOK(host); !ok || norm != host {
		return "", bad()
	}
	got, ok := t.host.match(host, nil)
	if !ok || len(got) != len(t.names) {
		return "", bad()
	}
	for i, name := range t.names {
		if v, _ := value(name); got[i] != v {
			return "", bad()
		}
	}
	return host, nil
}

// hasDotSegment reports a segment of path that is "." or "..", which a browser
// removes or resolves against the segment before it. PathEscape leaves the
// dots alone, and a literal of a pattern holds no '%', so the escaped forms
// such as "%2e" never reach here.
func hasDotSegment(path string) bool {
	for seg := range strings.SplitSeq(path, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

func needsRoundTrip(segs []segment) bool {
	return slices.ContainsFunc(segs, func(s segment) bool {
		return s.kind == segTemplate || s.kind == segConstraint || s.kind == segWildcard
	})
}

// routesBack reports whether path, as a request carries it, matches the
// segments of the template with the same values.
func (t *urlTemplate) routesBack(path string, value func(name string) (string, bool)) bool {
	u, err := url.ParseRequestURI(path)
	if err != nil {
		return false
	}
	p, escaped := requestPath(u)
	for len(p) > 1 && p[len(p)-1] == '/' {
		p = p[:len(p)-1]
	}
	if p == "/" {
		p = ""
	}
	want := func(name string) string {
		v, _ := value(name)
		return v
	}

	for _, sg := range t.segs {
		if sg.kind == segWildcard {
			rest := ""
			if p != "" {
				rest = p[1:]
			}
			decoded, ok := decodePathSegment(rest, escaped)
			return ok && decoded == want(sg.value)
		}
		if p == "" {
			return false
		}
		raw, tail := cutSegment(p)
		p = tail
		seg, ok := decodePathSegment(raw, escaped)
		if !ok {
			return false
		}
		switch sg.kind {
		case segStatic:
			if seg != sg.value {
				return false
			}
		case segTemplate:
			got := make([]string, templateArity(sg.parts))
			if !matchTemplate(got, sg.parts, seg) {
				return false
			}
			for i, n := range templateNames(sg.parts) {
				if got[i] != want(n) {
					return false
				}
			}
		case segConstraint:
			if !sg.m.match(seg) || seg != want(sg.value) {
				return false
			}
		default:
			if seg != want(sg.value) {
				return false
			}
		}
	}
	return p == ""
}

// escapeRest path-escapes a catch-all value one segment at a time, so its
// slashes stay separators.
func escapeRest(v string) string {
	if !strings.Contains(v, "/") {
		return url.PathEscape(v)
	}
	var b strings.Builder
	b.Grow(len(v))
	for i, seg := range strings.Split(v, "/") {
		if i > 0 {
			b.WriteByte('/')
		}
		b.WriteString(url.PathEscape(seg))
	}
	return b.String()
}

func quoteList(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = `"` + n + `"`
	}
	return strings.Join(out, ", ")
}

// parseURLTemplate cuts a pattern into literal text and parameters. It does not
// report an error: an unbalanced brace and what follows it stay literal text,
// which only a pattern the router refused can hold.
func parseURLTemplate(pattern string) []urlPart {
	var (
		parts []urlPart
		lit   int
	)
	for i := 0; i < len(pattern); {
		if pattern[i] != '{' {
			i++
			continue
		}
		end, ok := closingBrace(pattern, i)
		if !ok {
			break
		}
		if lit < i {
			parts = append(parts, urlPart{lit: pattern[lit:i]})
		}
		name, constraint, _ := strings.Cut(pattern[i+1:end], ":")
		name, rest := strings.CutSuffix(name, "...")
		parts = append(parts, urlPart{name: name, constraint: constraint, rest: rest})
		i, lit = end+1, end+1
	}
	if lit < len(pattern) {
		parts = append(parts, urlPart{lit: pattern[lit:]})
	}
	return parts
}
