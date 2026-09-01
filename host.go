package router

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
)

type hostLabel struct {
	lit   string
	parts []segPart
	whole *segPart
	off   int
	n     int
	rest  bool
}

type hostSpec struct {
	pattern string
	suffix  string
	labels  []hostLabel
	names   []string
	head    *segPart
	statics int
	any     bool
	hasRest bool
}

func (s *hostSpec) exact() bool { return !s.any && len(s.names) == 0 && s.statics == len(s.labels) }

type hostEntry[C Context] struct {
	hostSpec
	tree            *node[C]
	mws             []Middleware[C]
	haveMWs         bool
	idx             int32
	notFoundChain   HandlerFunc[C]
	notAllowedChain HandlerFunc[C]
	optionsChain    HandlerFunc[C]
	errHandler      ErrorHandlerFunc[C]
	rawNotFound     HandlerFunc[C]
	rawNotAllowed   HandlerFunc[C]
}

type hostSet[C Context] struct {
	exact map[string]*hostEntry[C]
	pats  []*hostEntry[C]
	any   *hostEntry[C]
	all   []*hostEntry[C]
}

func (hs *hostSet[C]) match(host string, dst []string) (*hostEntry[C], []string) {
	if e, ok := hs.exact[host]; ok {
		return e, dst
	}
	for _, e := range hs.pats {
		vals, ok := e.match(host, dst)
		if ok {
			return e, vals
		}
		clear(vals[len(dst):])
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
	if n := len(pattern); n > 0 && pattern[n-1] == '.' {
		pattern = pattern[:n-1]
	}
	pattern = lowerHostLiterals(pattern)
	if !hostLiteralsASCII(pattern) {
		return hostSpec{}, fmt.Errorf("router: host pattern %q must use ASCII or punycode literals", raw)
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

func hostLiteralsASCII(pattern string) bool {
	depth := 0
	for i := range len(pattern) {
		switch pattern[i] {
		case '{':
			depth++
		case '}':
			depth--
		default:
			if depth == 0 && pattern[i] >= 0x80 {
				return false
			}
		}
	}
	return true
}

func lowerHostLiterals(pattern string) string {
	depth := 0
	for i := range len(pattern) {
		switch pattern[i] {
		case '{':
			depth++
		case '}':
			depth--
		default:
			if depth == 0 && pattern[i] >= 'A' && pattern[i] <= 'Z' {
				b := []byte(pattern)
				depth = 0
				for j := range len(b) {
					switch b[j] {
					case '{':
						depth++
					case '}':
						depth--
					default:
						if depth == 0 && b[j] >= 'A' && b[j] <= 'Z' {
							b[j] += 'a' - 'A'
						}
					}
				}
				return string(b)
			}
		}
	}
	return pattern
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
	for i := range a.labels {
		ak, bk := hostLabelSpecificity(a.labels[i]), hostLabelSpecificity(b.labels[i])
		if ak != bk {
			return bk - ak
		}
	}
	return strings.Compare(hostShape(a), hostShape(b))
}

func hostLabelSpecificity(l hostLabel) int {
	switch {
	case l.lit != "":
		return 4
	case l.parts != nil && l.whole == nil:
		return 3
	case l.whole != nil && l.whole.re != nil:
		return 2
	case l.whole != nil:
		return 1
	default:
		return 0
	}
}

func hostShape(s *hostSpec) string {
	var b strings.Builder
	if s.any {
		return "*"
	}
	for i, l := range slices.Backward(s.labels) {
		if i != len(s.labels)-1 {
			b.WriteByte('.')
		}
		switch {
		case l.lit != "":
			b.WriteString("l:")
			b.WriteString(l.lit)
		case l.parts != nil && l.whole == nil:
			b.WriteString("t:")
			b.WriteString(templateSkeleton(l.parts))
		case l.whole != nil && l.whole.re != nil:
			b.WriteString("r:")
			b.WriteString(l.whole.re.String())
		case l.rest:
			b.WriteString("rest")
		case l.whole != nil:
			b.WriteString("param")
		default:
			b.WriteString("wild")
		}
	}
	return b.String()
}

func sameHostShape(a, b *hostSpec) bool { return hostShape(a) == hostShape(b) }

// "example.com." is the same host as "example.com", so a router that kept the
// dot would answer a request a Host header check was meant to catch.
func normalizeHost(host string) string {
	host, _ = normalizeHostOK(host)
	return host
}

func normalizeHostOK(authority string) (string, bool) {
	if authority == "" {
		return "", false
	}
	if authority[0] != '[' {
		labelStart := 0
		for i := 0; i < len(authority); i++ {
			c := authority[i]
			if c == '.' {
				if i == labelStart {
					return "", false
				}
				labelStart = i + 1
				continue
			}
			if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
				continue
			}
			return normalizeHostAuthoritySlow(authority)
		}
		if labelStart < len(authority) {
			return authority, true
		}
	}
	return normalizeHostAuthoritySlow(authority)
}

func normalizeHostAuthoritySlow(authority string) (string, bool) {
	if authority[0] == '[' {
		return normalizeIPAuthority(authority)
	}
	hostEnd := len(authority)
	labelStart := 0
	upper := false
	for i := 0; i < len(authority); i++ {
		c := authority[i]
		switch {
		case c == ':':
			hostEnd = i
			if !validPort(authority[i+1:]) {
				return "", false
			}
			i = len(authority)
		case c == '.':
			if i == labelStart {
				return "", false
			}
			labelStart = i + 1
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_':
		default:
			return "", false
		}
	}
	if hostEnd == 0 {
		return "", false
	}
	if labelStart == hostEnd {
		hostEnd--
		if hostEnd == 0 {
			return "", false
		}
	}
	host := authority[:hostEnd]
	if upper {
		host = asciiLower(host)
	}
	return host, true
}

func normalizeIPAuthority(authority string) (string, bool) {
	end := strings.IndexByte(authority, ']')
	if end < 0 {
		return "", false
	}
	host := authority[1:end]
	if strings.IndexByte(host, ':') < 0 || net.ParseIP(host) == nil {
		return "", false
	}
	rest := authority[end+1:]
	if rest != "" && (rest[0] != ':' || !validPort(rest[1:])) {
		return "", false
	}
	return asciiLower(host), true
}

func validPort(port string) bool {
	if port == "" {
		return false
	}
	n := 0
	for i := range len(port) {
		if port[i] < '0' || port[i] > '9' {
			return false
		}
		n = n*10 + int(port[i]-'0')
		if n > 65535 {
			return false
		}
	}
	return true
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
