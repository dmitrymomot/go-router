package router

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type hostLabel struct {
	lit   string
	parts []segPart

	whole *segPart

	off int
	n   int

	rest bool
}

type hostSpec struct {
	pattern string

	suffix string

	labels []hostLabel

	names []string

	head *segPart

	statics int
	any     bool
	hasRest bool
}

func (s *hostSpec) exact() bool { return !s.any && len(s.names) == 0 && s.statics == len(s.labels) }

type hostEntry[C Context] struct {
	hostSpec

	tree *node[C]

	mws     []Middleware[C]
	haveMWs bool

	idx int32

	notFoundChain   HandlerFunc[C]
	notAllowedChain HandlerFunc[C]
	optionsChain    HandlerFunc[C]
	errHandler      ErrorHandlerFunc[C]

	rawNotFound   HandlerFunc[C]
	rawNotAllowed HandlerFunc[C]
}

type hostSet[C Context] struct {
	exact map[string]*hostEntry[C]

	pats []*hostEntry[C]

	any *hostEntry[C]

	all []*hostEntry[C]
}

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
			return dst, false
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
		default:
			if seg == "" {
				return dst, false
			}
		}
	}
	if rest != "" {
		return dst, false
	}
	return dst, true
}

func parseHostPattern(pattern string) (hostSpec, error) {
	raw := pattern
	pattern = asciiLower(pattern)
	if n := len(pattern); n > 0 && pattern[n-1] == '.' {
		pattern = pattern[:n-1]
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

	for _, l := range slices.Backward(s.labels) {
		if l.parts != nil || l.lit == "" {
			if s.suffix != "" {
				s.suffix = "." + s.suffix
			}
			break
		}
		if s.suffix == "" {
			s.suffix = l.lit
		} else {
			s.suffix = l.lit + "." + s.suffix
		}
	}

	if n := len(s.labels); n > 1 && s.statics == n-1 && !s.hasRest {
		switch first := s.labels[0]; {
		case first.whole != nil:
			s.head = first.whole
		case first.parts == nil && first.lit == "":
			s.head = new(segPart)
		}
	}

	slices.Reverse(s.labels)
	return s, nil
}

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

// "example.com." is the same host as "example.com", so a router that kept the
// dot would answer a request a Host header check was meant to catch.
func normalizeHost(host string) string {
	if host == "" {
		return ""
	}
	if host[0] == '[' {
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

func asciiLower(s string) string {
	for i := range len(s) {
		if c := s[i]; c >= 'A' && c <= 'Z' {
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
