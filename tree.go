package router

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// methodHandler binds one HTTP method to the handler of a route.
type methodHandler[C Context] struct {
	method  string
	handler HandlerFunc[C]
}

// node is one segment of the route trie. A node holds its children grouped by
// the kind of segment that reaches them. The matcher tries the groups in the
// order static, regular expression, parameter, catch-all, so a literal path
// always wins over a parameter.
type node[C Context] struct {
	static    map[string]*node[C]
	templates []*node[C]
	regexes   []*node[C]
	param     *node[C]
	wildcard  *node[C]

	// seg describes the segment that reaches this node.
	seg segment

	routes  []methodHandler[C]
	pattern string
	names   []string
}

// child returns the existing child for seg, or nil. It reports an error when a
// child has the same shape under other parameter names, because two names for
// one position would make Param ambiguous.
func (n *node[C]) child(seg segment) (*node[C], error) {
	if seg.kind == segTemplate {
		want := templateSkeleton(seg.parts)
		names := templateNames(seg.parts)
		for _, c := range n.templates {
			if templateSkeleton(c.seg.parts) != want {
				continue
			}
			if !slices.Equal(templateNames(c.seg.parts), names) {
				return nil, fmt.Errorf("%q has the shape of %q, which already uses the parameter names %v",
					seg.value, c.seg.value, templateNames(c.seg.parts))
			}
			return c, nil
		}
		return nil, nil
	}

	c := n.plainChild(seg)
	if c != nil && seg.kind != segStatic && c.seg.value != seg.value {
		return nil, fmt.Errorf("the parameter at this position is already named %q, not %q",
			c.seg.value, seg.value)
	}
	return c, nil
}

// plainChild returns the existing child for a non-template segment, or nil.
func (n *node[C]) plainChild(seg segment) *node[C] {
	switch seg.kind {
	case segStatic:
		return n.static[seg.value]
	case segRegex:
		for _, c := range n.regexes {
			if c.seg.re.String() == seg.re.String() {
				return c
			}
		}
		return nil
	case segParam:
		return n.param
	default:
		return n.wildcard
	}
}

// addChild creates and links a child for seg.
func (n *node[C]) addChild(seg segment) *node[C] {
	c := &node[C]{seg: seg}
	switch seg.kind {
	case segStatic:
		if n.static == nil {
			n.static = make(map[string]*node[C], 4)
		}
		n.static[seg.value] = c
	case segTemplate:
		n.templates = append(n.templates, c)
	case segRegex:
		n.regexes = append(n.regexes, c)
	case segParam:
		n.param = c
	default:
		n.wildcard = c
	}
	return c
}

// insert adds a route to the trie. It reports an error when the pattern
// conflicts with a pattern that is already registered.
func (n *node[C]) insert(method, pattern string, h HandlerFunc[C]) error {
	segs, names, err := parsePattern(pattern)
	if err != nil {
		return err
	}

	cur := n
	for _, seg := range segs {
		next, err := cur.child(seg)
		if err != nil {
			return fmt.Errorf("router: %q conflicts with an existing route: %w", normalizePattern(pattern), err)
		}
		if next == nil {
			next = cur.addChild(seg)
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
	return nil
}

// handler returns the handler for the method. A HEAD request falls back to the
// GET handler, as net/http does.
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
	return nil
}

// allowed returns the methods that the node answers, in a stable order and
// with the implied HEAD and OPTIONS entries.
func (n *node[C]) allowed() []string {
	out := make([]string, 0, len(n.routes)+2)
	for _, mh := range n.routes {
		out = append(out, mh.method)
	}
	if slices.Contains(out, http.MethodGet) && !slices.Contains(out, http.MethodHead) {
		out = append(out, http.MethodHead)
	}
	if !slices.Contains(out, http.MethodOptions) {
		out = append(out, http.MethodOptions)
	}
	slices.Sort(out)
	return out
}

// matchState carries the information that a failed method lookup needs, so
// that the router can answer 405 instead of 404.
type matchState[C Context] struct {
	// pathMatch is a node whose pattern matched the path but which does not
	// answer the request method.
	pathMatch *node[C]

	// pathVals holds the parameter values of pathMatch. The walk continues
	// after it records them and overwrites the shared array, so they are a
	// copy.
	pathVals []string
}

// record stores the first node that matched the path but not the method.
func (st *matchState[C]) record(n *node[C], vals []string) {
	if st.pathMatch == nil && len(n.routes) > 0 {
		st.pathMatch = n
		st.pathVals = slices.Clone(vals)
	}
}

// search walks the trie. It returns the node that answers method for path and
// the parameter values in declaration order.
//
// vals shares one backing array across the whole walk. A branch that fails
// leaves values behind, and the next branch overwrites them at the same index,
// so only the values of the branch that succeeds reach the caller.
func search[C Context](n *node[C], path, method string, vals []string, st *matchState[C]) (*node[C], []string) {
	if path == "" || path == "/" {
		if n.handler(method) != nil {
			return n, vals
		}
		st.record(n, vals)
		// A catch-all also matches an empty remainder, as in /files for
		// the pattern /files/{path...}.
		if n.wildcard != nil {
			w := append(vals, "")
			if n.wildcard.handler(method) != nil {
				return n.wildcard, w
			}
			st.record(n.wildcard, w)
		}
		return nil, nil
	}

	seg, rest := splitSegment(path)

	if c, ok := n.static[seg]; ok {
		if m, v := search(c, rest, method, vals, st); m != nil {
			return m, v
		}
	}
	for _, c := range n.templates {
		if w, ok := appendTemplateValues(vals, c.seg.parts, seg); ok {
			if m, v := search(c, rest, method, w, st); m != nil {
				return m, v
			}
		}
	}
	for _, c := range n.regexes {
		if c.seg.re.MatchString(seg) {
			if m, v := search(c, rest, method, append(vals, seg), st); m != nil {
				return m, v
			}
		}
	}
	if n.param != nil && seg != "" {
		if m, v := search(n.param, rest, method, append(vals, seg), st); m != nil {
			return m, v
		}
	}
	if n.wildcard != nil {
		w := append(vals, path[1:])
		if n.wildcard.handler(method) != nil {
			return n.wildcard, w
		}
		st.record(n.wildcard, w)
	}
	return nil, nil
}

// walk calls fn for every registered route, in trie order.
func (n *node[C]) walk(fn func(pattern, method string)) {
	if n.pattern != "" {
		for _, mh := range n.routes {
			fn(n.pattern, mh.method)
		}
	}
	keys := make([]string, 0, len(n.static))
	for k := range n.static {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		n.static[k].walk(fn)
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

// maxParams returns the deepest parameter count in the trie.
func (n *node[C]) maxParams(depth int) int {
	switch {
	case n.seg.kind == segTemplate:
		depth += len(templateNames(n.seg.parts))
	case n.seg.kind != segStatic && n.seg.value != "":
		depth++
	}
	best := depth
	for _, c := range n.static {
		best = max(best, c.maxParams(depth))
	}
	for _, c := range n.templates {
		best = max(best, c.maxParams(depth))
	}
	for _, c := range n.regexes {
		best = max(best, c.maxParams(depth))
	}
	if n.param != nil {
		best = max(best, n.param.maxParams(depth))
	}
	if n.wildcard != nil {
		best = max(best, n.wildcard.maxParams(depth))
	}
	return best
}

// unescapeParams decodes percent escapes in place.
func unescapeParams(vals []string) {
	for i, v := range vals {
		if strings.IndexByte(v, '%') < 0 {
			continue
		}
		if u, err := url.PathUnescape(v); err == nil {
			vals[i] = u
		}
	}
}
