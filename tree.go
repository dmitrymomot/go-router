package router

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// methodHandler binds one HTTP method to the handler of a route.
type methodHandler[C Context] struct {
	method  string
	handler HandlerFunc[C]
}

// node is one node of the route tree.
//
// The tree is a compressed radix tree over the literal text of the patterns: a
// node consumes a run of literal characters that may span several segments, so
// a static route costs a handful of string comparisons rather than one lookup
// per segment. Every static child of a node starts with a different byte,
// which is what indices records, so picking the branch is a byte scan.
//
// A parameter, a regular expression, a template such as "rep-{date}.csv" and a
// catch-all each get their own child, because they consume by a rule that
// literal text cannot express. The matcher tries the children in the order
// literal, template, regular expression, parameter, catch-all, so a literal
// path always wins, and it backtracks when a branch dies further down.
type node[C Context] struct {
	// prefix is the literal text that this node consumes. It is empty for the
	// root and for every node that is not a literal.
	prefix string

	// indices holds the first byte of each entry of statics, in the same
	// order.
	indices string
	statics []*node[C]

	templates []*node[C]
	regexes   []*node[C]
	param     *node[C]
	wildcard  *node[C]

	// The fields below describe a node that is not a literal.
	kind  edgeKind
	name  string
	re    *regexp.Regexp
	parts []segPart
	raw   string

	routes []methodHandler[C]

	// catchAll is the handler of the entry that [Router.Any] registered, nil
	// for a node that carries none. It sits beside routes rather than in
	// them, so that a lookup for an explicit method never scans for it.
	catchAll HandlerFunc[C]

	pattern string
	names   []string
}

// insert adds a route to the tree. hostNames holds the parameter names of the
// host pattern that owns this tree, which run in front of the ones of the path.
// It reports an error when the pattern conflicts with a pattern that is already
// registered.
func (n *node[C]) insert(method, pattern string, hostNames []string, h HandlerFunc[C]) error {
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
	return nil
}

// insertLiteral walks the literal text into the tree and returns the node that
// ends it. It splits a node whose prefix only partly matches, which is what
// keeps every static child of a node starting with a different byte.
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
			// The new text branches away in the middle of this node. Keep the
			// shared head here and move everything else into a child.
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

// insertSpecial links the child that consumes a parameter, a regular
// expression, a template or a catch-all.
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

// namingConflict reports two names for one position, which would make Param
// ambiguous.
func namingConflict(existing, want string) error {
	return fmt.Errorf("the parameter at this position is already named %q, not %q", existing, want)
}

// handler returns the handler for the method. The exact method wins; a HEAD
// request then falls back to the GET handler, as net/http does, and any method
// falls back to the entry that [Router.Any] registered.
//
// The entry of Any keeps a field of its own instead of a place among the
// routes, because every request runs this lookup: a node that carries no such
// entry then answers with a nil field, and one that does never slows the
// method it names outright.
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

// allowed returns the methods that the node answers, in a stable order and
// with the implied HEAD and OPTIONS entries.
//
// The entry of [Router.Any] contributes nothing, because the router cannot
// name every method that it answers. A node that carries one never reaches
// this point anyway: it answers whatever arrives, so it produces no 405.
func (n *node[C]) allowed() []string {
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
	if !slices.Contains(out, http.MethodOptions) {
		out = append(out, http.MethodOptions)
	}
	slices.Sort(out)
	return out
}

// cacheAllow records the Allow header of every node that carries routes in
// dst. The build calls it once the trie is whole, so that nothing writes to
// the map after a request can reach it: the serving goroutines read it without
// a lock, and a lazy fill on the first 405 would race them.
//
// The header lives here rather than on the node because every matched request
// walks the trie and only a 405 and an auto-OPTIONS answer read it. A string
// field on the node widens every node the walk touches, which costs the whole
// hot path about two percent to spare the two cold ones.
func (n *node[C]) cacheAllow(dst map[*node[C]]string) {
	if len(n.routes) > 0 {
		dst[n] = strings.Join(n.allowed(), ", ")
	}
	for _, c := range n.statics {
		c.cacheAllow(dst)
	}
	for _, c := range n.templates {
		c.cacheAllow(dst)
	}
	for _, c := range n.regexes {
		c.cacheAllow(dst)
	}
	if n.param != nil {
		n.param.cacheAllow(dst)
	}
	if n.wildcard != nil {
		n.wildcard.cacheAllow(dst)
	}
}

// matchState carries the information that a failed method lookup needs, so
// that the router can answer 405 instead of 404.
type matchState[C Context] struct {
	// pathMatch is the first node whose pattern matched the path but which
	// does not answer the request method. Its pattern and its values are the
	// ones that [Base.RoutePattern] and [Base.Param] report.
	pathMatch *node[C]

	// pathVals holds the parameter values of pathMatch. The walk continues
	// after it records them and overwrites the shared array, so they are a
	// copy.
	pathVals []string

	// rest holds the further nodes that matched the path. The Allow header
	// names their methods too, because the walk backtracks to them: a literal
	// sibling is visited before the parameter and the catch-all underneath it,
	// and a request for one of their methods reaches them.
	//
	// It is a pointer because [Router.route] holds one matchState per walk in
	// its own frame, and every request pays for their size. A path that two
	// patterns match under different methods is rare enough that the slice
	// header does not earn a place there; the walk that finds one allocates.
	rest *[]*node[C]
}

// record stores a node that matched the path but not the method. Only the
// first one keeps its values, because only its pattern and its parameters
// reach the request.
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

// allowedMethods appends the methods of every node that matched the path,
// without repeating one.
func (st *matchState[C]) allowedMethods(dst []string) []string {
	if st.pathMatch == nil {
		return dst
	}
	add := func(n *node[C]) {
		for _, m := range n.allowed() {
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

// search walks the tree. n has already consumed its own part of the path, and
// rest is what remains. It returns the node that answers method and the
// parameter values in declaration order.
//
// vals shares one backing array across the whole walk. A branch that fails
// leaves values behind, and the next branch overwrites them at the same index,
// so only the values of the branch that succeeds reach the caller.
func search[C Context](n *node[C], rest, method string, vals []string, st *matchState[C]) (*node[C], []string) {
	if rest == "" {
		if n.handler(method) != nil {
			return n, vals
		}
		st.record(n, vals)
		// A catch-all also matches an empty remainder, as in /files for the
		// pattern /files/{path...}.
		if n.wildcard != nil {
			w := append(vals, "")
			if n.wildcard.handler(method) != nil {
				return n.wildcard, w
			}
			st.record(n.wildcard, w)
		}
		return nil, nil
	}

	// A short inline scan beats strings.IndexByte here: a node holds a handful
	// of literal children, and the call overhead outweighs the search.
	head := rest[0]
	for i := 0; i < len(n.indices); i++ {
		if n.indices[i] != head {
			continue
		}
		if c := n.statics[i]; strings.HasPrefix(rest, c.prefix) {
			if m, v := search(c, rest[len(c.prefix):], method, vals, st); m != nil {
				return m, v
			}
		}
		break
	}

	// Every other kind owns the separator in front of it, so none of them can
	// match in the middle of a segment. That check is also what keeps
	// /assets/* from answering /assetsfoo.
	if head != '/' {
		return nil, nil
	}

	if n.templates != nil || n.regexes != nil || n.param != nil {
		body := rest[1:]
		seg, tail := body, ""
		if i := strings.IndexByte(body, '/'); i >= 0 {
			seg, tail = body[:i], body[i:]
		}

		for _, c := range n.templates {
			if w, ok := appendTemplateValues(vals, c.parts, seg); ok {
				if m, v := search(c, tail, method, w, st); m != nil {
					return m, v
				}
			}
		}
		for _, c := range n.regexes {
			if c.re.MatchString(seg) {
				if m, v := search(c, tail, method, append(vals, seg), st); m != nil {
					return m, v
				}
			}
		}
		if n.param != nil && seg != "" {
			if m, v := search(n.param, tail, method, append(vals, seg), st); m != nil {
				return m, v
			}
		}
	}

	if n.wildcard != nil {
		w := append(vals, rest[1:])
		if n.wildcard.handler(method) != nil {
			return n.wildcard, w
		}
		st.record(n.wildcard, w)
	}
	return nil, nil
}

// walk calls fn for every registered route.
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
