package middleware

import (
	"net/url"
	"strings"

	"github.com/dmitrymomot/go-router"
)

type RewriteRule struct {
	Match string
	To    string
}

func Rewrite[C router.Context](rules ...RewriteRule) router.Middleware[C] {
	compiled := make([]rewriteRule, len(rules))
	for i, rule := range rules {
		if rule.Match == "" {
			panic("middleware: a rewrite rule needs a Match")
		}
		parts := strings.Split(rule.Match, "*")
		for i := range parts {
			parts[i] = escapeRewriteText(parts[i])
		}
		compiled[i] = rewriteRule{parts: parts, to: escapeRewriteTemplate(rule.To)}
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			req := c.Request()
			// The path does not change between rules, so decode it once.
			path := decodeUnreservedEscapes(req.URL.EscapedPath())
			for _, rule := range compiled {
				to, ok := rule.apply(path)
				if !ok {
					continue
				}
				path, err := url.PathUnescape(to)
				if err != nil {
					continue
				}
				rewritten, u := *req, *req.URL
				u.Path, u.RawPath = path, to
				if path == to {
					u.RawPath = ""
				}
				rewritten.URL = &u
				c.SetRequest(&rewritten)
				break
			}
			return next(c)
		}
	}
}

func escapeRewriteText(s string) string {
	return (&url.URL{Path: s}).EscapedPath()
}

func escapeRewriteTemplate(s string) string {
	if !strings.Contains(s, "$") {
		return escapeRewriteText(s)
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		j := strings.IndexByte(s[i:], '$')
		if j < 0 {
			b.WriteString(escapeRewriteText(s[i:]))
			break
		}
		j += i
		b.WriteString(escapeRewriteText(s[i:j]))
		if j+1 < len(s) && s[j+1] >= '1' && s[j+1] <= '9' {
			b.WriteString(s[j : j+2])
			i = j + 2
			continue
		}
		b.WriteByte('$')
		i = j + 1
	}
	return b.String()
}

// decodeUnreservedEscapes decodes the escapes standing for an unreserved
// character and leaves every other one alone, so a rule sees one spelling of
// the path it matches. The router has its own canonicalEscapedPath, which keeps
// only the escapes for a separator, a backslash and a percent: this one is the
// narrower rule, and the two are not interchangeable.
func decodeUnreservedEscapes(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if i+2 >= len(s) || s[i] != '%' {
			b.WriteByte(s[i])
			i++
			continue
		}
		v, ok := rewriteUnhex(s[i+1], s[i+2])
		if !ok {
			b.WriteByte(s[i])
			i++
			continue
		}
		if rewriteUnreserved(v) {
			b.WriteByte(v)
		} else {
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[v>>4])
			b.WriteByte(hex[v&15])
		}
		i += 3
	}
	return b.String()
}

func rewriteUnhex(a, b byte) (byte, bool) {
	hex := func(c byte) (byte, bool) {
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
	hi, ok := hex(a)
	if !ok {
		return 0, false
	}
	lo, ok := hex(b)
	return hi<<4 | lo, ok
}

func rewriteUnreserved(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '-' || c == '.' || c == '_' || c == '~'
}

func rewriteBoundary(s string, i int) bool {
	if i < 0 || i > len(s) {
		return false
	}
	if i > 0 && i+1 < len(s) && s[i-1] == '%' {
		_, ok := rewriteUnhex(s[i], s[i+1])
		if ok {
			return false
		}
	}
	if i > 1 && i < len(s) && s[i-2] == '%' {
		_, ok := rewriteUnhex(s[i-1], s[i])
		if ok {
			return false
		}
	}
	return true
}

func rewriteIndex(s, part string) int {
	for i := 0; i <= len(s); {
		if strings.HasPrefix(s[i:], part) && rewriteBoundary(s, i+len(part)) {
			return i
		}
		if i == len(s) {
			break
		}
		if i+2 < len(s) && s[i] == '%' {
			if _, ok := rewriteUnhex(s[i+1], s[i+2]); ok {
				i += 3
				continue
			}
		}
		i++
	}
	return -1
}

type rewriteRule struct {
	parts []string
	to    string
}

func (r rewriteRule) apply(p string) (string, bool) {
	if len(r.parts) == 1 {
		return r.to, p == r.parts[0]
	}

	first := r.parts[0]
	if !strings.HasPrefix(p, first) || !rewriteBoundary(p, len(first)) {
		return "", false
	}
	rest := p[len(first):]
	var arr [9]string
	caps := arr[:0]
	for _, part := range r.parts[1 : len(r.parts)-1] {
		i := rewriteIndex(rest, part)
		if i < 0 {
			return "", false
		}
		caps = append(caps, rest[:i])
		rest = rest[i+len(part):]
	}
	last := r.parts[len(r.parts)-1]
	start := len(rest) - len(last)
	if start < 0 || !rewriteBoundary(rest, start) || !strings.HasSuffix(rest, last) {
		return "", false
	}
	return expandRewrite(r.to, append(caps, rest[:start])), true
}

func expandRewrite(to string, caps []string) string {
	if !strings.Contains(to, "$") {
		return to
	}
	var b strings.Builder
	b.Grow(len(to))
	for i := 0; i < len(to); {
		if to[i] == '$' && i+1 < len(to) && to[i+1] >= '1' && to[i+1] <= '9' {
			if n := int(to[i+1] - '1'); n < len(caps) {
				b.WriteString(caps[n])
			}
			i += 2
			continue
		}
		b.WriteByte(to[i])
		i++
	}
	return b.String()
}
