package router

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
)

type namedRoute struct {
	pattern string
	parts   []urlPart
	segs    []segment
	recheck bool
}

type urlPart struct {
	lit  string
	name string
	rest bool
}

func (r *Router[C]) URL(name string, params map[string]string) (string, error) {
	root := r.root
	if err := root.compile(); err != nil {
		return "", err
	}
	nr, ok := root.named[name]
	if !ok {
		return "", fmt.Errorf("router: no route is named %q", name)
	}
	return expandURL(name, nr, params)
}

func (r *Router[C]) MustURL(name string, kv ...string) string {
	if len(kv)%2 != 0 {
		panic(fmt.Sprintf("router: MustURL(%q) needs alternating keys and values, but got %d arguments", name, len(kv)))
	}
	var params map[string]string
	if len(kv) > 0 {
		params = make(map[string]string, len(kv)/2)
		for i := 0; i < len(kv); i += 2 {
			params[kv[i]] = kv[i+1]
		}
	}
	out, err := r.URL(name, params)
	if err != nil {
		panic(err.Error())
	}
	return out
}

func expandURL(name string, nr namedRoute, params map[string]string) (string, error) {
	var (
		b        strings.Builder
		used     int
		trimTail bool
	)
	b.Grow(len(nr.pattern))
	for _, p := range nr.parts {
		if p.name == "" {
			b.WriteString(p.lit)
			continue
		}
		v, ok := params[p.name]
		if !ok {
			return "", fmt.Errorf("router: the route %q needs the parameter %q of %q", name, p.name, nr.pattern)
		}
		used++
		switch {
		case p.rest && v == "":
			trimTail = true
		case p.rest:
			b.WriteString(escapeRest(v))
		case v == "":
			return "", fmt.Errorf("router: the parameter %q of the route %q is empty, and %q matches no path with an empty segment", p.name, name, nr.pattern)
		default:
			b.WriteString(url.PathEscape(v))
		}
	}
	if used != len(params) {
		return "", fmt.Errorf("router: the route %q takes no parameter %s in %q", name, quoteList(spareParams(nr.parts, params)), nr.pattern)
	}
	out := b.String()
	if trimTail {
		if out = strings.TrimSuffix(out, "/"); out == "" {
			out = "/"
		}
	}
	if nr.recheck {
		if err := checkRoundTrip(name, nr, out, params); err != nil {
			return "", err
		}
	}
	return out, nil
}

func needsRoundTrip(segs []segment) bool {
	return slices.ContainsFunc(segs, func(s segment) bool {
		return s.kind == segTemplate || s.kind == segRegex || s.kind == segWildcard
	})
}

func checkRoundTrip(name string, nr namedRoute, path string, params map[string]string) error {
	bad := func() error {
		return fmt.Errorf("router: the route %q builds %q, which %q does not match with those values", name, path, nr.pattern)
	}

	u, err := url.ParseRequestURI(path)
	if err != nil {
		return bad()
	}
	p, escaped := requestPath(u)
	for len(p) > 1 && p[len(p)-1] == '/' {
		p = p[:len(p)-1]
	}
	if p == "/" {
		p = ""
	}

	for _, sg := range nr.segs {
		if sg.kind == segWildcard {
			rest := ""
			if p != "" {
				rest = p[1:]
			}
			if !sameValue(rest, params[sg.value], escaped) {
				return bad()
			}
			return nil
		}
		if p == "" {
			return bad()
		}
		seg, tail := cutSegment(p)
		p = tail

		switch sg.kind {
		case segStatic:
			if seg != sg.value {
				return bad()
			}
		case segTemplate:
			got := make([]string, templateArity(sg.parts))
			if !matchTemplate(got, sg.parts, seg) {
				return bad()
			}
			for i, n := range templateNames(sg.parts) {
				if !sameValue(got[i], params[n], escaped) {
					return bad()
				}
			}
		case segRegex:
			if !sg.re.MatchString(seg) || !sameValue(seg, params[sg.value], escaped) {
				return bad()
			}
		default:
			if !sameValue(seg, params[sg.value], escaped) {
				return bad()
			}
		}
	}
	if p != "" {
		return bad()
	}
	return nil
}

func sameValue(got, want string, escaped bool) bool {
	if escaped {
		if v, err := url.PathUnescape(got); err == nil {
			got = v
		}
	}
	return got == want
}

func escapeRest(v string) string {
	if !strings.Contains(v, "/") {
		return url.PathEscape(v)
	}
	var b strings.Builder
	b.Grow(len(v))
	sep := ""
	for seg := range strings.SplitSeq(v, "/") {
		b.WriteString(sep)
		b.WriteString(url.PathEscape(seg))
		sep = "/"
	}
	return b.String()
}

func spareParams(parts []urlPart, params map[string]string) []string {
	var out []string
	for k := range params {
		if !slices.ContainsFunc(parts, func(p urlPart) bool { return p.name == k }) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func quoteList(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = `"` + n + `"`
	}
	return strings.Join(out, ", ")
}

func parseURLTemplate(pattern string) []urlPart {
	var (
		parts []urlPart
		lit   int
	)
	for i := 0; i < len(pattern); {
		switch pattern[i] {
		case '{':
			end, ok := closingBrace(pattern, i)
			if !ok {
				return append(parts, urlPart{lit: pattern[lit:]})
			}
			if lit < i {
				parts = append(parts, urlPart{lit: pattern[lit:i]})
			}
			body := pattern[i+1 : end]
			name, _, _ := strings.Cut(body, ":")
			name, rest := strings.CutSuffix(name, "...")
			parts = append(parts, urlPart{name: name, rest: rest})
			i, lit = end+1, end+1

		case '*':
			if lit < i {
				parts = append(parts, urlPart{lit: pattern[lit:i]})
			}
			parts = append(parts, urlPart{name: mountParam, rest: true})
			i, lit = i+1, i+1

		default:
			i++
		}
	}
	if lit < len(pattern) {
		parts = append(parts, urlPart{lit: pattern[lit:]})
	}
	return parts
}
