package router

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
)

type scopeFallback[C Context] struct {
	prefix          string
	rec             *routeRecord
	names           []string
	pattern         []segment
	depth           int
	hostIdx         int32
	errIdx          int32
	notFoundChain   HandlerFunc[C]
	notAllowedChain HandlerFunc[C]
	optionsChain    HandlerFunc[C]
}

func (s *scopeFallback[C]) covers(path string, escaped bool) bool {
	_, ok := s.walk(path, escaped, nil)
	return ok
}

// walk matches the scope prefix against the path, collecting the value of every
// non-static segment when vals is non-nil. covers passes nil: it must not
// allocate.
func (s *scopeFallback[C]) walk(path string, escaped bool, vals []string) ([]string, bool) {
	for _, want := range s.pattern {
		if want.kind == segWildcard {
			if vals != nil {
				vals = append(vals, strings.TrimPrefix(path, "/"))
			}
			return vals, true
		}
		if path == "" {
			return vals, false
		}
		raw, rest := cutSegment(path)
		got := raw
		if want.kind != segStatic {
			var ok bool
			got, ok = decodePathSegment(raw, escaped)
			if !ok {
				return vals, false
			}
		}
		switch {
		case vals != nil && want.kind == segTemplate:
			// One value per parameter of the segment, as search binds them.
			var ok bool
			if vals, ok = appendTemplateValues(vals, want.parts, got); !ok {
				return vals, false
			}
		case !segmentMatches(want, got):
			return vals, false
		case vals != nil && want.kind != segStatic:
			vals = append(vals, got)
		}
		path = rest
	}
	return vals, true
}

func cutSegment(p string) (seg, rest string) {
	p = p[1:]
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], p[i:]
	}
	return p, ""
}

func scopeFor[C Context](scopes []*scopeFallback[C], host *hostEntry[C], path string, escaped bool) *scopeFallback[C] {
	if len(scopes) == 0 {
		return nil
	}
	idx := int32(-1)
	if host != nil {
		idx = host.idx
	}
	for _, s := range scopes {
		if s.hostIdx == idx && s.covers(path, escaped) {
			return s
		}
	}
	return nil
}

// fallbackChains picks the 404, 405 and OPTIONS answers for a path no route
// took, and the error handler that owns them.
func (e *engine[C]) fallbackChains(
	host *hostEntry[C], path string, escaped bool,
) (scope *scopeFallback[C], notFound, notAllowed, options HandlerFunc[C], errIdx int32) {
	if s := scopeFor(e.scopes, host, path, escaped); s != nil {
		return s, s.notFoundChain, s.notAllowedChain, s.optionsChain, s.errIdx
	}
	if host != nil {
		return nil, host.notFoundChain, host.notAllowedChain, host.optionsChain, host.errIdx
	}
	return nil, e.notFoundChain, e.notAllowedChain, e.optionsChain, 0
}

// bindPrefixParams gives a scope fallback the parameters of its own prefix, so
// a 404 under /t/{tid} can read the tenant. There is no matched route here.
func (s *scopeFallback[C]) bindPrefixParams(b *Base, path string, escaped bool) {
	if len(s.names) == 0 {
		return
	}
	// walk collects only into a non-nil slice; a request that matched no host
	// has none, so the inline array gives it somewhere to write.
	seed := b.paramVals
	if seed == nil {
		seed = b.paramArr[:0]
	}
	vals, ok := s.walk(path, escaped, seed)
	if !ok {
		return
	}
	names := s.names
	if len(b.paramNames) > 0 {
		names = append(slices.Clip(b.paramNames), s.names...)
	}
	b.needsCleanup = true
	b.setRoute(s.rec, names, vals)
}

func autoOptions[C Context](c C) error { return c.base().NoContent(http.StatusNoContent) }

func (e *engine[C]) allowHeader(host, anyHost *matchState[C]) string {
	if host.rest == nil && anyHost.rest == nil {
		var only *node[C]
		switch {
		case host.pathMatch != nil && anyHost.pathMatch == nil:
			only = host.pathMatch
		case anyHost.pathMatch != nil && host.pathMatch == nil:
			only = anyHost.pathMatch
		}
		if only != nil {
			if s, ok := e.allowCache[only]; ok {
				return s
			}
		}
	}
	out := host.allowedMethods(nil, e.autoOptions)
	out = anyHost.allowedMethods(out, e.autoOptions)
	slices.Sort(out)
	return strings.Join(out, ", ")
}

// canMatch reports whether the path without its trailing slash reaches a
// route that the slash redirect may point at. A catch-all, and an Any route
// such as a MountHandler, takes the path with its slash and decides itself:
// a file server answers a directory with that slash, and stripping it would
// loop.
func (e *engine[C]) canMatch(host *hostEntry[C], path, method string, scratch []string, escaped bool) bool {
	if host != nil {
		var st matchState[C]
		n, _ := search(host.tree, path, method, scratch, &st, escaped)
		if st.redirectable(n) {
			return true
		}
	}
	if !e.anyHostRoutes {
		return false
	}
	var st matchState[C]
	n, _ := search(e.tree, path, method, scratch, &st, escaped)
	return st.redirectable(n)
}

// redirectable reports whether n, the route search found, or a node that
// matched the path for another method, is one the slash redirect may point at.
func (st *matchState[C]) redirectable(n *node[C]) bool {
	if n != nil && n.slashRedirectable() {
		return true
	}
	if st.pathMatch == nil {
		return false
	}
	if st.pathMatch.slashRedirectable() {
		return true
	}
	if st.rest != nil {
		for _, m := range *st.rest {
			if m.slashRedirectable() {
				return true
			}
		}
	}
	return false
}

func (n *node[C]) slashRedirectable() bool {
	return n.kind != edgeWildcard && (n.catchAll == 0 || len(n.routes) > 1)
}

// A second leading separator collapses: "//evil.com/" arrives as a path, and a
// Location that keeps it is a network-path reference to another origin. 308
// rather than 301 for a body-carrying method, which some clients turn into GET.
func redirectTo(w http.ResponseWriter, req *http.Request, path string, escaped bool) {
	for len(path) > 1 && path[1] == '/' {
		path = path[1:]
	}
	u := *req.URL
	u.Path, u.RawPath = path, ""
	if escaped {
		u.RawPath = path
		if unescaped, err := url.PathUnescape(path); err == nil {
			u.Path = unescaped
		}
	}
	status := http.StatusMovedPermanently
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		status = http.StatusPermanentRedirect
	}
	// Not http.Redirect: it runs path.Clean, which resolves a dot segment the
	// trie matched literally and so points at another route.
	w.Header().Set(HeaderLocation, u.RequestURI())
	w.WriteHeader(status)
}

// hasDotSegmentEscaped is hasDotSegment for a path that may still hold its
// escapes: a browser resolves "%2e" and "%2E%2e" as it resolves "." and "..".
func hasDotSegmentEscaped(path string) bool {
	for seg := range strings.SplitSeq(path, "/") {
		switch strings.ToLower(seg) {
		case ".", "..", "%2e", ".%2e", "%2e.", "%2e%2e":
			return true
		}
	}
	return false
}
