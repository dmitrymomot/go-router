package router

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// hostLabel is one "." delimited part of a host pattern.
//
// A label is static when parts is nil and lit is not empty, an anonymous
// wildcard when both are empty, and a parameter or a template otherwise. The
// matcher walks the labels from the right, so the tail of a pattern, which is
// the part that a host shares with its neighbours, decides early.
type hostLabel struct {
	lit   string    // static text; empty for a wildcard, a parameter or a template
	parts []segPart // the pieces of a parameter or a template label

	// whole is the parameter of a label that is nothing but "{name}", which is
	// the shape that almost every host pattern uses. It takes the label as it
	// stands, so the general template walk stays out of the hot path.
	whole *segPart

	// off is the index of the first value of this label inside the values of
	// the pattern, which run in declaration order. n is how many it holds.
	off int
	n   int

	// rest reports a "{name...}" label, which takes one or more labels. Only
	// the leftmost label of a pattern may be one.
	rest bool
}

// hostSpec is a parsed host pattern.
type hostSpec struct {
	// pattern is the pattern as the matcher normalized it: lower case, with no
	// port and no trailing dot. [Route] and [Base.RouteHost] report it.
	pattern string

	// suffix is the run of static labels at the end of the pattern, such as
	// ".example.com" for "{tenant}.example.com". A host that does not end in
	// it cannot match, so one comparison rejects most patterns.
	suffix string

	// labels runs from right to left. It is nil for an exact pattern and for
	// the "*" pattern, which both match without a walk.
	labels []hostLabel

	// names holds the parameter names in declaration order, which is left to
	// right.
	names []string

	// head is the parameter of a pattern whose only non-static label is the
	// leftmost one, such as "{tenant}.example.com". That shape carries most of
	// the multi-tenant traffic, so it matches with one suffix comparison and
	// one byte scan instead of a walk. It is nil for every other shape, and it
	// points at an empty segPart for "*.example.com", which keeps no value.
	head *segPart

	statics int  // the number of static labels, which ranks specificity
	any     bool // the "*" pattern, which matches every host
	hasRest bool
}

// exact reports whether the pattern is one fixed host, which the matcher finds
// with a map lookup.
func (s *hostSpec) exact() bool { return !s.any && len(s.names) == 0 && s.statics == len(s.labels) }

// hostEntry is one host pattern together with the routes that answer it. Each
// entry owns a route tree, so a request costs one host lookup and then the
// same trie walk that a router without host routes performs.
type hostEntry[C Context] struct {
	hostSpec

	tree *node[C]

	// mws is the middleware of the scope that opened this host, which wraps
	// the fallbacks below. haveMWs marks it as recorded, because an empty
	// chain is a legitimate value.
	mws     []Middleware[C]
	haveMWs bool

	// idx is the position of this entry inside hostSet.all. [Base] records it,
	// which is how the error handler finds the entry again.
	idx int32

	// The fallbacks of the host scope. Each one is nil until the scope sets
	// it, and the router then falls back to the one of the root.
	notFoundChain   HandlerFunc[C]
	notAllowedChain HandlerFunc[C]
	optionsChain    HandlerFunc[C]
	errHandler      ErrorHandlerFunc[C]
}

// hostSet resolves a request host to the entry that answers it.
type hostSet[C Context] struct {
	// exact maps a fixed host to its entry. It is nil while no pattern is
	// fixed, because a lookup in a nil map costs less than one in an empty
	// map.
	exact map[string]*hostEntry[C]

	// pats holds the patterns that need a walk, most specific first.
	pats []*hostEntry[C]

	// any is the entry of the "*" pattern, which answers a host that no other
	// pattern claims.
	any *hostEntry[C]

	// all holds every entry in registration order. [Base] records the index of
	// the matched entry, which is how the error handler finds it again.
	all []*hostEntry[C]
}

// match returns the entry that answers host and appends the values of its host
// parameters, in declaration order.
func (hs *hostSet[C]) match(host string, dst []string) (*hostEntry[C], []string) {
	if e, ok := hs.exact[host]; ok {
		return e, dst
	}
	for _, e := range hs.pats {
		if vals, ok := e.match(host, dst); ok {
			return e, vals
		}
	}
	if hs.any != nil {
		return hs.any, dst
	}
	return nil, dst
}

// match reports whether host fits the pattern and appends the parameter values
// in declaration order.
//
// It walks the labels from the right, because that is the end that a host
// shares with its neighbours: ".example.com" rejects an unrelated domain on
// the first comparison, and a leading "{name...}" then takes whatever is left
// without any backtracking.
func (e *hostEntry[C]) match(host string, dst []string) ([]string, bool) {
	if !strings.HasSuffix(host, e.suffix) {
		return dst, false
	}

	if e.head != nil {
		label := host[:len(host)-len(e.suffix)]
		if label == "" || strings.IndexByte(label, '.') >= 0 {
			return dst, false
		}
		if e.head.re != nil && !e.head.re.MatchString(label) {
			return dst, false
		}
		if e.head.name != "" {
			dst = append(dst, label)
		}
		return dst, true
	}

	base := len(dst)
	for range e.names {
		dst = append(dst, "")
	}
	out := dst[base:]

	rest := host
	for _, l := range e.labels {
		if rest == "" {
			return dst, false // the pattern wants more labels than the host has
		}
		var seg string
		if j := strings.LastIndexByte(rest, '.'); j >= 0 && !l.rest {
			seg, rest = rest[j+1:], rest[:j]
		} else {
			seg, rest = rest, ""
		}

		switch {
		case l.whole != nil:
			if seg == "" || (l.whole.re != nil && !l.whole.re.MatchString(seg)) {
				return dst, false
			}
			out[l.off] = seg
		case l.parts != nil:
			if !matchTemplate(out[l.off:l.off+l.n], l.parts, seg) {
				return dst, false
			}
		case l.lit != "":
			if seg != l.lit {
				return dst, false
			}
		default: // "*", which takes any one label but keeps no value
			if seg == "" {
				return dst, false
			}
		}
	}
	if rest != "" {
		return dst, false // the host carries labels that the pattern does not
	}
	return dst, true
}

// ---------------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------------

// parseHostPattern reads a host pattern. It reports an error for a malformed
// one, which [Router.Host] turns into a panic at registration time.
//
// Supported syntax:
//
//	example.com                  that host exactly
//	{tenant}.example.com         one label, readable as c.Param("tenant")
//	{tenant:[a-z0-9-]+}.ex.com   one label that the regular expression accepts
//	acme-{env}.example.com       part of a label
//	{sub...}.example.com         one or more leading labels
//	*.example.com                one label, with no value kept
//	*                            any host, for a custom domain
func parseHostPattern(pattern string) (hostSpec, error) {
	raw := pattern
	pattern = asciiLower(pattern) // the same folding that a request host gets
	if n := len(pattern); n > 0 && pattern[n-1] == '.' {
		pattern = pattern[:n-1] // a fully qualified name, as a browser may send it
	}
	if pattern == "" {
		return hostSpec{}, fmt.Errorf("router: a host pattern must not be empty")
	}
	if pattern == "*" {
		return hostSpec{pattern: "*", any: true}, nil
	}
	if indexOutsideBraces(pattern, ':') >= 0 {
		return hostSpec{}, fmt.Errorf("router: host pattern %q must not carry a port; the router matches the host without one", raw)
	}

	s := hostSpec{pattern: pattern}
	labels, err := splitHostLabels(pattern)
	if err != nil {
		return hostSpec{}, err
	}
	for i, text := range labels {
		l, names, err := parseHostLabel(text, pattern)
		if err != nil {
			return hostSpec{}, err
		}
		if l.rest && i != 0 {
			return hostSpec{}, fmt.Errorf("router: %q must be the first label in %q", text, pattern)
		}
		for _, n := range names {
			if slices.Contains(s.names, n) {
				return hostSpec{}, fmt.Errorf("router: duplicate parameter %q in %q", n, pattern)
			}
		}
		if len(l.parts) == 1 && l.parts[0].lit == "" {
			l.whole = &l.parts[0]
		}
		l.off, l.n = len(s.names), len(names)
		s.names = append(s.names, names...)
		s.hasRest = s.hasRest || l.rest
		if l.parts == nil && l.lit != "" {
			s.statics++
		}
		s.labels = append(s.labels, l)
	}

	// The static tail rejects an unrelated host in one comparison, so record it
	// before the labels flip.
	for _, l := range slices.Backward(s.labels) {
		if l.parts != nil || l.lit == "" {
			if s.suffix != "" {
				s.suffix = "." + s.suffix // the dot that separates it from this label
			}
			break
		}
		if s.suffix == "" {
			s.suffix = l.lit
		} else {
			s.suffix = l.lit + "." + s.suffix
		}
	}

	// Detect the shape that the fast path above handles: one leading label that
	// takes a whole label, and nothing but static text behind it.
	if n := len(s.labels); n > 1 && s.statics == n-1 && !s.hasRest {
		switch first := s.labels[0]; {
		case first.whole != nil:
			s.head = first.whole
		case first.parts == nil && first.lit == "":
			s.head = new(segPart) // "*", which keeps no value
		}
	}

	slices.Reverse(s.labels)
	return s, nil
}

// splitHostLabels cuts a host pattern at the dots that sit outside braces, so
// that a dot inside a regular expression stays where the author put it.
func splitHostLabels(pattern string) ([]string, error) {
	var (
		labels []string
		start  int
		depth  int
	)
	for i := range len(pattern) {
		switch pattern[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("router: unbalanced braces in %q", pattern)
			}
		case '.':
			if depth > 0 {
				continue
			}
			if i == start {
				return nil, fmt.Errorf("router: empty label in %q", pattern)
			}
			labels = append(labels, pattern[start:i])
			start = i + 1
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("router: unbalanced braces in %q", pattern)
	}
	if start == len(pattern) {
		return nil, fmt.Errorf("router: empty label in %q", pattern)
	}
	return append(labels, pattern[start:]), nil
}

// parseHostLabel reads one label of a host pattern and returns the parameter
// names that it declares.
func parseHostLabel(text, pattern string) (hostLabel, []string, error) {
	switch {
	case text == "*":
		return hostLabel{}, nil, nil

	case isWholeBrace(text):
		name, expr, rest, err := parseBraceSegment(text, pattern)
		if err != nil {
			return hostLabel{}, nil, err
		}
		if rest {
			return hostLabel{parts: []segPart{{name: name}}, rest: true}, []string{name}, nil
		}
		var re *regexp.Regexp
		if expr != "" {
			re, err = regexp.Compile("^(?:" + expr + ")$")
			if err != nil {
				return hostLabel{}, nil, fmt.Errorf("router: bad regular expression for %q in %q: %w", name, pattern, err)
			}
		}
		return hostLabel{parts: []segPart{{name: name, re: re}}}, []string{name}, nil

	case strings.ContainsAny(text, "{}"):
		parts, names, err := parseTemplate(text, pattern)
		if err != nil {
			return hostLabel{}, nil, err
		}
		return hostLabel{parts: parts}, names, nil

	case strings.ContainsRune(text, '*'):
		return hostLabel{}, nil, fmt.Errorf("router: a wildcard must span a whole label, but %q does not in %q", text, pattern)

	default:
		return hostLabel{lit: text}, nil, nil
	}
}

// indexOutsideBraces returns the index of the first b that sits outside a
// "{...}" group, or -1.
func indexOutsideBraces(s string, b byte) int {
	depth := 0
	for i := range len(s) {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		case b:
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// lessSpecific orders two patterns, most specific first. A pattern with more
// static labels wins, then one that takes a fixed number of labels, then the
// longer one. The pattern text breaks the remaining ties, so the order does not
// depend on the order of registration.
func lessSpecific(a, b *hostSpec) int {
	if a.statics != b.statics {
		return b.statics - a.statics
	}
	if a.hasRest != b.hasRest {
		if a.hasRest {
			return 1
		}
		return -1
	}
	if len(a.labels) != len(b.labels) {
		return len(b.labels) - len(a.labels)
	}
	return strings.Compare(a.pattern, b.pattern)
}

// ---------------------------------------------------------------------------
// Request hosts
// ---------------------------------------------------------------------------

// normalizeHost returns the host that the patterns match against: no port, no
// trailing dot, lower case. It allocates only for a host that carries an upper
// case letter, which almost none do.
//
// The trailing dot matters beyond tidiness. "example.com." is the same host as
// "example.com", so a router that kept the dot would answer a request that a
// check on the Host header was meant to catch.
func normalizeHost(host string) string {
	if host == "" {
		return ""
	}
	if host[0] == '[' { // an IPv6 literal, which carries the port outside the brackets
		if i := strings.IndexByte(host, ']'); i >= 0 {
			host = host[1:i]
		}
	} else if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	if n := len(host); n > 1 && host[n-1] == '.' {
		host = host[:n-1]
	}
	return asciiLower(host)
}

// asciiLower lowercases the ASCII letters of s and returns s itself when it
// holds none, which is the case for nearly every request.
//
// It lowercases bytes rather than runes on purpose. A host name is ASCII on
// the wire, because a name outside it arrives in punycode, and a byte pass
// cannot fold two distinct names into one.
func asciiLower(s string) string {
	for i := range len(s) {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			// Everything before i is already lower case, so only the tail
			// needs rewriting.
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if c := b[j]; c >= 'A' && c <= 'Z' {
					b[j] = c + ('a' - 'A')
				}
			}
			return string(b)
		}
	}
	return s
}
