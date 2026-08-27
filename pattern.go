package router

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// segKind is the kind of one pattern segment.
type segKind uint8

const (
	segStatic segKind = iota
	segTemplate
	segRegex
	segParam
	segWildcard
)

// segment is one "/" delimited part of a route pattern.
type segment struct {
	kind  segKind
	value string // static text, the parameter name, or the raw template text
	re    *regexp.Regexp

	// parts holds the pieces of a template segment such as "rep-{date}.csv".
	parts []segPart
}

// segPart is one piece of a template segment: either literal text or a
// parameter that runs up to the next literal.
type segPart struct {
	lit  string // empty for a parameter
	name string
	re   *regexp.Regexp
}

// mountParam is the parameter name that [Router.MountHandler] uses for the
// part of the path that it strips before it calls the mounted handler.
const mountParam = "*"

// normalizePattern gives a pattern a leading "/" and removes a trailing "/".
// The root pattern stays "/".
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

// joinPattern joins a prefix and a pattern into one normalized pattern.
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

// parsePattern splits a route pattern into segments and returns the parameter
// names in the order in which the pattern declares them. It reports an error
// for a malformed pattern.
//
// Supported syntax:
//
//	/users              a static segment
//	/users/{id}         a named parameter that matches one segment
//	/users/{id:[0-9]+}  a named parameter with a regular expression
//	/files/{path...}    a named catch-all, which must be the last segment
//	/files/*            an anonymous catch-all, named "*"
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
			segs = append(segs, segment{kind: segStatic, value: raw})
		}
	}
	return segs, names, nil
}

// isWholeBrace reports whether the segment is a single "{...}" group with no
// text around it.
func isWholeBrace(raw string) bool {
	if !strings.HasPrefix(raw, "{") {
		return false
	}
	end, ok := closingBrace(raw, 0)
	return ok && end == len(raw)-1
}

// closingBrace returns the index of the "}" that closes the "{" at start. It
// counts depth, so a quantifier inside a regular expression does not end the
// group early.
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

// parseTemplate reads a segment that mixes literal text with parameters, such
// as "rep-{date}.csv" or "{name}.json".
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

// appendTemplateValues matches seg against a template and appends the
// parameter values in declaration order.
//
// Literals are matched as far to the right as the segment allows, so a
// parameter takes as much as it can. "rep-{date}.csv" reads "a.csv" out of
// "rep-a.csv.csv", and "{name}.{ext}" splits "a.b.txt" into "a.b" and "txt".
//
// No parameter may be empty, which is what keeps "rep-.csv" from matching.
func appendTemplateValues(dst []string, parts []segPart, seg string) ([]string, bool) {
	lo, hi := 0, len(seg)

	// Peel the literals at both ends. What is left alternates parameter,
	// literal, parameter, and starts and ends with a parameter.
	first, last := 0, len(parts)-1
	if parts[first].lit != "" {
		if !strings.HasPrefix(seg, parts[first].lit) {
			return dst, false
		}
		lo = len(parts[first].lit)
		first++
	}
	if last >= first && parts[last].lit != "" {
		lit := parts[last].lit
		if hi-len(lit) < lo || !strings.HasSuffix(seg[:hi], lit) {
			return dst, false
		}
		hi -= len(lit)
		last--
	}
	if first > last {
		return dst, false
	}

	base := len(dst)
	for range (last-first)/2 + 1 {
		dst = append(dst, "")
	}

	// Walk the inner literals from the right, so each parameter keeps as much
	// of the segment as it can.
	end := hi
	for i := last - 1; i >= first; i -= 2 {
		lit := parts[i].lit
		j := strings.LastIndex(seg[lo:end], lit)
		if j < 0 {
			return dst, false
		}
		if !setTemplateValue(dst, base+(i+1-first)/2, parts[i+1], seg[lo+j+len(lit):end]) {
			return dst, false
		}
		end = lo + j
	}
	return dst, setTemplateValue(dst, base, parts[first], seg[lo:end])
}

// setTemplateValue stores one parameter value after it checks that the value
// is not empty and that the regular expression, if any, accepts it.
func setTemplateValue(dst []string, i int, p segPart, value string) bool {
	if value == "" || (p.re != nil && !p.re.MatchString(value)) {
		return false
	}
	dst[i] = value
	return true
}

// templateSkeleton describes the shape of a template without its parameter
// names, so that the trie can tell two spellings of one shape apart.
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

// templateNames returns the parameter names of a template, in order.
func templateNames(parts []segPart) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.lit == "" {
			out = append(out, p.name)
		}
	}
	return out
}

// parseBraceSegment reads a "{...}" segment. It returns the parameter name, an
// optional regular expression, and whether the segment is a catch-all.
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

// splitSegment splits a path that starts with "/" into its first segment and
// the remainder, which is empty or starts with "/".
func splitSegment(path string) (seg, rest string) {
	p := path[1:]
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], p[i:]
	}
	return p, ""
}
