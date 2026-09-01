package router

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

type methodHandler[C Context] struct {
	method  string
	handler HandlerFunc[C]
}

type node[C Context] struct {
	prefix  string
	indices string
	statics []*node[C]

	templates []*node[C]
	regexes   []*node[C]
	param     *node[C]
	wildcard  *node[C]
	kind      edgeKind
	name      string
	re        *regexp.Regexp
	parts     []segPart
	raw       string

	routes   []methodHandler[C]
	catchAll HandlerFunc[C]
	allow    string

	pattern string
	names   []string
}

func (n *node[C]) insert(method, pattern string, hostNames []string, h HandlerFunc[C], autoOptions bool) error {
	segs, names, err := parsePattern(pattern)
	if err != nil {
		return err
	}
	if len(hostNames) > 0 {
		for _, hn := range hostNames {
			if slices.Contains(names, hn) {
				return fmt.Errorf("router: parameter %q of %q is already declared by the host pattern", hn, normalizePattern(pattern))
			}
		}
		names = append(slices.Clone(hostNames), names...)
	}

	cur := n
	for _, e := range patternEdges(segs) {
		if e.kind == edgeLiteral {
			cur = cur.insertLiteral(e.lit)
			continue
		}
		next, err := cur.insertSpecial(e)
		if err != nil {
			return fmt.Errorf("router: %q conflicts with an existing route: %w", normalizePattern(pattern), err)
		}
		cur = next
	}

	if cur.pattern == "" {
		cur.pattern = normalizePattern(pattern)
		cur.names = names
	}
	for _, mh := range cur.routes {
		if mh.method == method {
			return fmt.Errorf("router: %s %q is already registered", method, normalizePattern(pattern))
		}
	}
	cur.routes = append(cur.routes, methodHandler[C]{method: method, handler: h})
	if method == anyMethod {
		cur.catchAll = h
	}
	cur.allow = strings.Join(cur.allowed(autoOptions), ", ")
	return nil
}

func (n *node[C]) insertLiteral(text string) *node[C] {
	cur := n
	for text != "" {
		i := strings.IndexByte(cur.indices, text[0])
		if i < 0 {
			child := &node[C]{kind: edgeLiteral, prefix: text}
			cur.indices += string(text[0])
			cur.statics = append(cur.statics, child)
			return child
		}

		child := cur.statics[i]
		shared := commonPrefixLen(child.prefix, text)
		if shared < len(child.prefix) {
			tail := new(node[C])
			*tail = *child
			tail.prefix = child.prefix[shared:]

			*child = node[C]{kind: edgeLiteral, prefix: child.prefix[:shared]}
			child.indices = string(tail.prefix[0])
			child.statics = []*node[C]{tail}
		}
		text = text[shared:]
		cur = child
	}
	return cur
}

func (n *node[C]) insertSpecial(e edge) (*node[C], error) {
	switch e.kind {
	case edgeTemplate:
		want := templateSkeleton(e.parts)
		names := templateNames(e.parts)
		for _, c := range n.templates {
			if templateSkeleton(c.parts) != want {
				continue
			}
			if !slices.Equal(templateNames(c.parts), names) {
				return nil, fmt.Errorf("%q has the shape of %q, which already uses the parameter names %v",
					e.raw, c.raw, templateNames(c.parts))
			}
			return c, nil
		}
		c := &node[C]{kind: edgeTemplate, name: e.name, parts: e.parts, raw: e.raw}
		n.templates = append(n.templates, c)
		return c, nil

	case edgeRegex:
		for _, c := range n.regexes {
			if c.re.String() != e.re.String() {
				continue
			}
			if c.name != e.name {
				return nil, namingConflict(c.name, e.name)
			}
			return c, nil
		}
		c := &node[C]{kind: edgeRegex, name: e.name, re: e.re}
		n.regexes = append(n.regexes, c)
		return c, nil

	case edgeWildcard:
		if n.wildcard == nil {
			n.wildcard = &node[C]{kind: edgeWildcard, name: e.name}
		} else if n.wildcard.name != e.name {
			return nil, namingConflict(n.wildcard.name, e.name)
		}
		return n.wildcard, nil

	default:
		if n.param == nil {
			n.param = &node[C]{kind: edgeParam, name: e.name}
		} else if n.param.name != e.name {
			return nil, namingConflict(n.param.name, e.name)
		}
		return n.param, nil
	}
}

func namingConflict(existing, want string) error {
	return fmt.Errorf("the parameter at this position is already named %q, not %q", existing, want)
}

func (n *node[C]) handler(method string) HandlerFunc[C] {
	for _, mh := range n.routes {
		if mh.method == method {
			return mh.handler
		}
	}
	if method == http.MethodHead {
		for _, mh := range n.routes {
			if mh.method == http.MethodGet {
				return mh.handler
			}
		}
	}
	return n.catchAll
}

func (n *node[C]) allowed(autoOptions bool) []string {
	out := make([]string, 0, len(n.routes)+2)
	for _, mh := range n.routes {
		if mh.method == anyMethod {
			continue
		}
		out = append(out, mh.method)
	}
	if slices.Contains(out, http.MethodGet) && !slices.Contains(out, http.MethodHead) {
		out = append(out, http.MethodHead)
	}
	if autoOptions && !slices.Contains(out, http.MethodOptions) {
		out = append(out, http.MethodOptions)
	}
	slices.Sort(out)
	return out
}

// insert keeps every node's Allow string current, so this only runs when the
// OPTIONS setting changes after some routes are already in.
func (n *node[C]) recacheAllow(autoOptions bool) {
	if len(n.routes) > 0 {
		n.allow = strings.Join(n.allowed(autoOptions), ", ")
	}
	for _, c := range n.statics {
		c.recacheAllow(autoOptions)
	}
	for _, c := range n.templates {
		c.recacheAllow(autoOptions)
	}
	for _, c := range n.regexes {
		c.recacheAllow(autoOptions)
	}
	if n.param != nil {
		n.param.recacheAllow(autoOptions)
	}
	if n.wildcard != nil {
		n.wildcard.recacheAllow(autoOptions)
	}
}

type matchState[C Context] struct {
	pathMatch *node[C]
	pathVals  []string
	rest      *[]*node[C]
}

func (st *matchState[C]) record(n *node[C], vals []string) {
	if len(n.routes) == 0 {
		return
	}
	if st.pathMatch == nil {
		st.pathMatch = n
		st.pathVals = slices.Clone(vals)
		return
	}
	if n == st.pathMatch {
		return
	}
	if st.rest == nil {
		st.rest = new([]*node[C])
	}
	if !slices.Contains(*st.rest, n) {
		*st.rest = append(*st.rest, n)
	}
}

func (st *matchState[C]) allowedMethods(dst []string, autoOptions bool) []string {
	if st.pathMatch == nil {
		return dst
	}
	add := func(n *node[C]) {
		for _, m := range n.allowed(autoOptions) {
			if !slices.Contains(dst, m) {
				dst = append(dst, m)
			}
		}
	}
	add(st.pathMatch)
	if st.rest != nil {
		for _, n := range *st.rest {
			add(n)
		}
	}
	return dst
}

func search[C Context](n *node[C], rest, method string, vals []string, st *matchState[C], escaped bool) (*node[C], []string) {
	if rest == "" {
		if n.handler(method) != nil {
			return n, vals
		}
		st.record(n, vals)
		if n.wildcard != nil {
			w := append(vals, "")
			if n.wildcard.handler(method) != nil {
				return n.wildcard, w
			}
			st.record(n.wildcard, w)
			clear(w[len(vals):])
		}
		return nil, nil
	}

	head := rest[0]
	for i := 0; i < len(n.indices); i++ {
		if n.indices[i] != head {
			continue
		}
		if c := n.statics[i]; strings.HasPrefix(rest, c.prefix) {
			if m, v := search(c, rest[len(c.prefix):], method, vals, st, escaped); m != nil {
				return m, v
			}
		}
		break
	}

	if head != '/' {
		return nil, nil
	}

	if n.templates != nil || n.regexes != nil || n.param != nil {
		body := rest[1:]
		seg, tail := body, ""
		if i := strings.IndexByte(body, '/'); i >= 0 {
			seg, tail = body[:i], body[i:]
		}
		decoded := seg
		if escaped {
			var ok bool
			decoded, ok = decodePathSegment(seg, true)
			if !ok {
				return nil, nil
			}
		}

		for _, c := range n.templates {
			w, ok := appendTemplateValues(vals, c.parts, decoded)
			if ok {
				if m, v := search(c, tail, method, w, st, escaped); m != nil {
					return m, v
				}
			}
			clear(w[len(vals):])
		}
		for _, c := range n.regexes {
			if c.re.MatchString(decoded) {
				w := append(vals, decoded)
				if m, v := search(c, tail, method, w, st, escaped); m != nil {
					return m, v
				}
				clear(w[len(vals):])
			}
		}
		if n.param != nil && decoded != "" {
			w := append(vals, decoded)
			if m, v := search(n.param, tail, method, w, st, escaped); m != nil {
				return m, v
			}
			clear(w[len(vals):])
		}
	}

	if n.wildcard != nil {
		w := append(vals, rest[1:])
		if n.wildcard.handler(method) != nil {
			return n.wildcard, w
		}
		st.record(n.wildcard, w)
		clear(w[len(vals):])
	}
	return nil, nil
}

func (n *node[C]) walk(fn func(pattern, method string)) {
	if n.pattern != "" {
		for _, mh := range n.routes {
			fn(n.pattern, mh.method)
		}
	}
	for _, c := range n.statics {
		c.walk(fn)
	}
	for _, c := range n.templates {
		c.walk(fn)
	}
	for _, c := range n.regexes {
		c.walk(fn)
	}
	if n.param != nil {
		n.param.walk(fn)
	}
	if n.wildcard != nil {
		n.wildcard.walk(fn)
	}
}

func decodePathSegment(seg string, escaped bool) (string, bool) {
	if !escaped || strings.IndexByte(seg, '%') < 0 {
		return seg, true
	}
	decoded, err := url.PathUnescape(seg)
	return decoded, err == nil
}
