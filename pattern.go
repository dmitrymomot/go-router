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
	segRegex
	segParam
	segWildcard
)

// segment is one "/" delimited part of a route pattern.
type segment struct {
	kind  segKind
	value string // static text, or the parameter name
	re    *regexp.Regexp
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

		case strings.HasPrefix(raw, "{"):
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

		default:
			if strings.ContainsAny(raw, "{}*") {
				return nil, nil, fmt.Errorf("router: a parameter must span a whole segment, but %q does not in %q", raw, pattern)
			}
			segs = append(segs, segment{kind: segStatic, value: raw})
		}
	}
	return segs, names, nil
}

// parseBraceSegment reads a "{...}" segment. It returns the parameter name, an
// optional regular expression, and whether the segment is a catch-all.
func parseBraceSegment(raw, pattern string) (name, expr string, wildcard bool, err error) {
	depth := 0
	end := -1
	for i := range len(raw) {
		switch raw[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i
			}
		}
	}
	if end != len(raw)-1 {
		return "", "", false, fmt.Errorf("router: a parameter must span a whole segment, but %q does not in %q", raw, pattern)
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
