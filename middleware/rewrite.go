package middleware

import (
	"strings"

	"github.com/dmitrymomot/go-router"
)

// RewriteRule is one rule of [Rewrite].
type RewriteRule struct {
	// Match is the path that the rule answers. A "*" in it stands for any run
	// of characters, and it is the only wildcard: no regular expression, so a
	// rule reads as the paths it rewrites.
	Match string

	// To is the path that the rule rewrites to. "$1" holds what the first
	// wildcard of Match took, "$2" the second, up to "$9". A "$1" that no
	// wildcard fills expands to nothing.
	To string
}

// Rewrite rewrites the request path.
//
// Add it with [router.Router.Pre], which is the stage that runs before the
// router matches. [router.Router.Use] runs after the match, where a new path
// reaches the handler and the log line but no longer picks the route:
//
//	r.Pre(middleware.Rewrite[Ctx](
//		middleware.RewriteRule{Match: "/api/v1/*", To: "/v1/$1"},
//		middleware.RewriteRule{Match: "/legacy", To: "/"},
//	))
//
// The rules are an ordered slice, and the first one that matches wins. That is
// the whole reason for the slice: a map of rules leaves the answer to two
// rules that both match up to the iteration order, which differs from run to
// run.
//
// The path is rewritten once. The result goes to the router as it is and never
// back through the rules, so two rules cannot chain, and one that rewrites a
// path onto itself is not a loop.
//
// A wildcard reads to the end of the run it can, and one in the middle of a
// pattern stops at the first literal that follows it: "/files/*.txt" reads
// "/files/a.b.txt" as "a.b", and the first wildcard of "/x/*/y/*" stops at the
// first "/y/".
//
// Rewrite reads and writes the path alone. The query string, the method and
// every header reach the handler untouched, and [router.Base.Path] answers the
// rewritten path, which is what the log line and [router.Router.Pre] describe.
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
				// RawPath holds the escaped form of the path that arrived, and
				// it describes the old path alone.
				u.Path, u.RawPath = to, ""
				rewritten.URL = &u
				c.SetRequest(&rewritten)
				break
			}
			return next(c)
		}
	}
}

// rewriteRule is a [RewriteRule] with its Match split at the wildcards, which
// is the form that matching reads.
type rewriteRule struct {
	// parts are the literal pieces of Match. A pattern without a wildcard has
	// one of them, and each wildcard adds another.
	parts []string
	to    string
}

// apply returns the path that the rule rewrites p to, and reports whether the
// rule matches p at all.
func (r rewriteRule) apply(p string) (string, bool) {
	if len(r.parts) == 1 {
		return r.to, p == r.parts[0]
	}

	rest, ok := strings.CutPrefix(p, r.parts[0])
	if !ok {
		return "", false
	}
	// Nine captures is what the replacement can name, and a rule that carries
	// more wildcards than that still matches.
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

// expandRewrite replaces "$1" to "$9" in to with what the wildcards took. A
// "$" that no digit follows stays as it is, which is what a path that carries
// one needs.
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
