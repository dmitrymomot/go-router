package middleware

import (
	"strings"

	"github.com/dmitrymomot/go-router"
)

type RewriteRule struct {
	Match string

	To string
}

func Rewrite[C router.Context](rules ...RewriteRule) router.Middleware[C] {
	compiled := make([]rewriteRule, len(rules))
	for i, rule := range rules {
		if rule.Match == "" {
			panic("middleware: a rewrite rule needs a Match")
		}
		compiled[i] = rewriteRule{parts: strings.Split(rule.Match, "*"), to: rule.To}
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			req := c.Request()
			for _, rule := range compiled {
				to, ok := rule.apply(req.URL.Path)
				if !ok {
					continue
				}
				rewritten, u := *req, *req.URL
				u.Path, u.RawPath = to, ""
				rewritten.URL = &u
				c.SetRequest(&rewritten)
				break
			}
			return next(c)
		}
	}
}

type rewriteRule struct {
	parts []string
	to    string
}

func (r rewriteRule) apply(p string) (string, bool) {
	if len(r.parts) == 1 {
		return r.to, p == r.parts[0]
	}

	rest, ok := strings.CutPrefix(p, r.parts[0])
	if !ok {
		return "", false
	}
	var arr [9]string
	caps := arr[:0]
	for _, part := range r.parts[1 : len(r.parts)-1] {
		i := strings.Index(rest, part)
		if i < 0 {
			return "", false
		}
		caps = append(caps, rest[:i])
		rest = rest[i+len(part):]
	}
	last := r.parts[len(r.parts)-1]
	if !strings.HasSuffix(rest, last) {
		return "", false
	}
	return expandRewrite(r.to, append(caps, rest[:len(rest)-len(last)])), true
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
