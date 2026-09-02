package router

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type segKind uint8

const (
	segStatic segKind = iota
	segTemplate
	segRegex
	segParam
	segWildcard
)

type segment struct {
	kind  segKind
	value string
	re    *regexp.Regexp
	parts []segPart
}

type segPart struct {
	lit  string
	name string
	re   *regexp.Regexp
}

const mountParam = "*"

func normalizePattern(p string) string {
	if p == "" {
		return "/"
	}
	if p[0] != '/' {
		p = "/" + p
	}
	for len(p) > 1 && p[len(p)-1] == '/' {
		p = p[:len(p)-1]
	}
	return p
}

func joinPattern(prefix, pattern string) string {
	prefix = normalizePattern(prefix)
	pattern = normalizePattern(pattern)
	if prefix == "/" {
		return pattern
	}
	if pattern == "/" {
		return prefix
	}
	return prefix + pattern
}

// ValidatePattern reports whether pattern is well formed, so that a table read
// from configuration can be checked before registration, which panics.
//
// It sees the pattern alone. Handle rejects more than this: a parameter the
// scope prefix or the host pattern already names, a route that collides with
// one registered earlier, and a name a scope has already used. A nil here does
// not promise that Handle will accept the pattern in every scope.
func ValidatePattern(pattern string) error {
	_, _, err := parsePattern(pattern)
	return err
}

func parsePattern(pattern string) ([]segment, []string, error) {
	pattern = normalizePattern(pattern)
	if pattern == "/" {
		return nil, nil, nil
	}

	var (
		segs  []segment
		names []string
	)
	for raw := range strings.SplitSeq(pattern[1:], "/") {
		if len(segs) > 0 && segs[len(segs)-1].kind == segWildcard {
			return nil, nil, fmt.Errorf("router: catch-all must be the last segment in %q", pattern)
		}

		switch {
		case raw == "*":
			// {*} spells the same name through the brace branch, which checks
			// for duplicates. This one did not, so "/{*}/*" registered with two
			// parameters called "*": Param returned only the first and URL
			// expansion failed on the second.
			if slices.Contains(names, mountParam) {
				return nil, nil, fmt.Errorf("router: duplicate parameter %q in %q", mountParam, pattern)
			}
			segs = append(segs, segment{kind: segWildcard, value: mountParam})
			names = append(names, mountParam)

		case isWholeBrace(raw):
			name, expr, wildcard, err := parseBraceSegment(raw, pattern)
			if err != nil {
				return nil, nil, err
			}
			if slices.Contains(names, name) {
				return nil, nil, fmt.Errorf("router: duplicate parameter %q in %q", name, pattern)
			}
			names = append(names, name)
			switch {
			case wildcard:
				segs = append(segs, segment{kind: segWildcard, value: name})
			case expr != "":
				re, err := regexp.Compile("^(?:" + expr + ")$")
				if err != nil {
					return nil, nil, fmt.Errorf("router: bad regular expression for %q in %q: %w", name, pattern, err)
				}
				segs = append(segs, segment{kind: segRegex, value: name, re: re})
			default:
				segs = append(segs, segment{kind: segParam, value: name})
			}

		case strings.ContainsAny(raw, "{}"):
			parts, partNames, err := parseTemplate(raw, pattern)
			if err != nil {
				return nil, nil, err
			}
			for _, n := range partNames {
				if slices.Contains(names, n) {
					return nil, nil, fmt.Errorf("router: duplicate parameter %q in %q", n, pattern)
				}
				names = append(names, n)
			}
			segs = append(segs, segment{kind: segTemplate, value: raw, parts: parts})

		default:
			if strings.Contains(raw, "*") {
				return nil, nil, fmt.Errorf("router: a catch-all must span a whole segment, but %q does not in %q", raw, pattern)
			}
			if err := checkStaticLiteral(raw, pattern); err != nil {
				return nil, nil, err
			}
			segs = append(segs, segment{kind: segStatic, value: raw})
		}
	}
	return segs, names, nil
}

// checkStaticLiteral rejects a literal segment that no request can reach.
//
// A path arrives at the trie in the canonical form requestPath produces: %, \
// and / stay percent-encoded with upper-case hex, every other escape is
// decoded, and a % that starts no escape is left alone. A literal is compared
// against that form byte for byte, so it has to be spelled the same way. The
// three spellings that can never match used to register happily and answer 404
// for ever with nothing to say why.
func checkStaticLiteral(raw, pattern string) error {
	for i := range len(raw) {
		if raw[i] == '\\' {
			return fmt.Errorf(
				"router: %q in %q can never match: the path arrives with a backslash escaped, so write %%5C",
				raw, pattern)
		}
		if raw[i] != '%' || i+2 >= len(raw) {
			continue
		}
		v, ok := unhex(raw[i+1], raw[i+2])
		if !ok {
			// requestPath leaves this alone, so the literal reaches the trie
			// exactly as written and matches.
			continue
		}
		if v != '/' && v != '\\' && v != '%' {
			return fmt.Errorf(
				"router: %q in %q can never match: the path arrives with %%%s decoded, so write %q",
				raw, pattern, raw[i+1:i+3], string(rune(v)))
		}
		const hex = "0123456789ABCDEF"
		if raw[i+1] != hex[v>>4] || raw[i+2] != hex[v&15] {
			return fmt.Errorf(
				"router: %q in %q can never match: the path arrives with upper-case escapes, so write %%%c%c",
				raw, pattern, hex[v>>4], hex[v&15])
		}
	}
	return nil
}

// unhex decodes one percent-escape body, reporting whether both digits were hex.
func unhex(a, b byte) (byte, bool) {
	hi, ok := hexDigit(a)
	if !ok {
		return 0, false
	}
	lo, ok := hexDigit(b)
	if !ok {
		return 0, false
	}
	return hi<<4 | lo, true
}

func hexDigit(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

func isWholeBrace(raw string) bool {
	if !strings.HasPrefix(raw, "{") {
		return false
	}
	end, ok := closingBrace(raw, 0)
	return ok && end == len(raw)-1
}

func closingBrace(raw string, start int) (int, bool) {
	depth := 0
	for i := start; i < len(raw); i++ {
		switch raw[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

func parseTemplate(raw, pattern string) ([]segPart, []string, error) {
	var (
		parts []segPart
		names []string
		lit   strings.Builder
	)
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, segPart{lit: lit.String()})
			lit.Reset()
		}
	}

	for i := 0; i < len(raw); {
		if raw[i] != '{' {
			if raw[i] == '}' {
				return nil, nil, fmt.Errorf("router: unbalanced %q in %q", raw, pattern)
			}
			lit.WriteByte(raw[i])
			i++
			continue
		}

		end, ok := closingBrace(raw, i)
		if !ok {
			return nil, nil, fmt.Errorf("router: unbalanced %q in %q", raw, pattern)
		}
		body := raw[i+1 : end]
		i = end + 1

		name, expr, _ := strings.Cut(body, ":")
		switch {
		case name == "":
			return nil, nil, fmt.Errorf("router: empty parameter name in %q", pattern)
		case strings.HasSuffix(body, "..."):
			return nil, nil, fmt.Errorf("router: a catch-all must span a whole segment, but %q does not in %q", raw, pattern)
		}

		var re *regexp.Regexp
		if expr != "" {
			compiled, err := regexp.Compile("^(?:" + expr + ")$")
			if err != nil {
				return nil, nil, fmt.Errorf("router: bad regular expression for %q in %q: %w", name, pattern, err)
			}
			re = compiled
		}

		flush()
		if n := len(parts); n > 0 && parts[n-1].lit == "" {
			return nil, nil, fmt.Errorf("router: %q puts two parameters side by side in %q; separate them with text", raw, pattern)
		}
		parts = append(parts, segPart{name: name, re: re})
		names = append(names, name)
	}
	flush()

	if len(names) == 0 {
		return nil, nil, fmt.Errorf("router: %q holds no parameter in %q", raw, pattern)
	}
	return parts, names, nil
}

func appendTemplateValues(dst []string, parts []segPart, seg string) ([]string, bool) {
	base := len(dst)
	for range templateArity(parts) {
		dst = append(dst, "")
	}
	return dst, matchTemplate(dst[base:], parts, seg)
}

func templateArity(parts []segPart) int {
	n := 0
	for _, p := range parts {
		if p.lit == "" {
			n++
		}
	}
	return n
}

func matchTemplate(out []string, parts []segPart, seg string) bool {
	lo, hi := 0, len(seg)

	first, last := 0, len(parts)-1
	if parts[first].lit != "" {
		if !strings.HasPrefix(seg, parts[first].lit) {
			return false
		}
		lo = len(parts[first].lit)
		first++
	}
	if last >= first && parts[last].lit != "" {
		lit := parts[last].lit
		if hi-len(lit) < lo || !strings.HasSuffix(seg[:hi], lit) {
			return false
		}
		hi -= len(lit)
		last--
	}
	if first > last {
		return false
	}

	end := hi
	for i := last - 1; i >= first; i -= 2 {
		lit := parts[i].lit
		j := strings.LastIndex(seg[lo:end], lit)
		if j < 0 {
			return false
		}
		if !setTemplateValue(out, (i+1-first)/2, parts[i+1], seg[lo+j+len(lit):end]) {
			return false
		}
		end = lo + j
	}
	return setTemplateValue(out, 0, parts[first], seg[lo:end])
}

func setTemplateValue(dst []string, i int, p segPart, value string) bool {
	if value == "" || (p.re != nil && !p.re.MatchString(value)) {
		return false
	}
	dst[i] = value
	return true
}

func segmentMatches(seg segment, value string) bool {
	switch seg.kind {
	case segStatic:
		return value == seg.value
	case segTemplate:
		return matchTemplate(make([]string, templateArity(seg.parts)), seg.parts, value)
	case segRegex:
		return seg.re.MatchString(value)
	case segParam:
		return value != ""
	default:
		return true
	}
}

func segmentSpecificity(kind segKind) int {
	switch kind {
	case segStatic:
		return 4
	case segTemplate:
		return 3
	case segRegex:
		return 2
	case segParam:
		return 1
	default:
		return 0
	}
}

func templateSkeleton(parts []segPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.lit != "" {
			b.WriteString(p.lit)
			continue
		}
		b.WriteByte(0)
		if p.re != nil {
			b.WriteString(p.re.String())
		}
		b.WriteByte(0)
	}
	return b.String()
}

func templateNames(parts []segPart) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.lit == "" {
			out = append(out, p.name)
		}
	}
	return out
}

func parseBraceSegment(raw, pattern string) (name, expr string, wildcard bool, err error) {
	end, ok := closingBrace(raw, 0)
	if !ok || end != len(raw)-1 {
		return "", "", false, fmt.Errorf("router: unbalanced %q in %q", raw, pattern)
	}

	body := raw[1:end]
	if name, expr, ok := strings.Cut(body, ":"); ok {
		if name == "" {
			return "", "", false, fmt.Errorf("router: empty parameter name in %q", pattern)
		}
		return name, expr, false, nil
	}
	if after, ok := strings.CutSuffix(body, "..."); ok {
		if after == "" {
			return "", "", false, fmt.Errorf("router: empty parameter name in %q", pattern)
		}
		return after, "", true, nil
	}
	if body == "" {
		return "", "", false, fmt.Errorf("router: empty parameter name in %q", pattern)
	}
	return body, "", false, nil
}

type edgeKind uint8

const (
	edgeLiteral edgeKind = iota
	edgeTemplate
	edgeRegex
	edgeParam
	edgeWildcard
)

type edge struct {
	kind  edgeKind
	lit   string
	name  string
	re    *regexp.Regexp
	parts []segPart
	raw   string
}

func patternEdges(segs []segment) []edge {
	var (
		edges []edge
		lit   strings.Builder
	)
	flush := func() {
		if lit.Len() > 0 {
			edges = append(edges, edge{kind: edgeLiteral, lit: lit.String()})
			lit.Reset()
		}
	}

	if len(segs) == 0 {
		return []edge{{kind: edgeLiteral, lit: "/"}}
	}

	for _, sg := range segs {
		switch sg.kind {
		case segStatic:
			lit.WriteByte('/')
			lit.WriteString(sg.value)
		case segWildcard:
			flush()
			edges = append(edges, edge{kind: edgeWildcard, name: sg.value})
		case segTemplate:
			flush()
			edges = append(edges, edge{kind: edgeTemplate, name: sg.value, parts: sg.parts, raw: sg.value})
		case segRegex:
			flush()
			edges = append(edges, edge{kind: edgeRegex, name: sg.value, re: sg.re})
		default:
			flush()
			edges = append(edges, edge{kind: edgeParam, name: sg.value})
		}
	}
	flush()
	return edges
}

func commonPrefixLen(a, b string) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}
