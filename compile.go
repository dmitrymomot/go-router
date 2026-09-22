package router

import (
	"slices"
	"strings"
)

// settingChanged rebuilds the derived state so the router is ready to serve the
// moment the setter returns.
func (r *Router[C]) settingChanged() {
	if root := r.top(); root.refreshDepth == 0 {
		root.refresh()
	}
}

// refresh recompiles the live table in place. Every setter that can change a
// fallback calls it, so the router is ready to serve the moment the setter
// returns.
func (r *Router[C]) refresh() { r.compile(r.eng) }

// inOneScope holds the graph rebuild until the outermost scope callback
// returns. refresh walks the whole graph, so one per nested Route, Group or
// Hosts made building a table quadratic in its size. The router is still ready
// to serve the moment that outermost call returns.
func (r *Router[C]) inOneScope(fn func()) {
	root := r.top()
	root.refreshDepth++
	defer func() {
		root.refreshDepth--
		if root.refreshDepth == 0 {
			root.refresh()
		}
	}()
	fn()
}

// freeze closes the graph for registration on the first request.
// Idempotent: it reads the graph and stores a flag, so two first requests can
// run it at once without a Once to serialize them.
func (r *Router[C]) freeze() {
	r.regMu.Lock()
	defer r.regMu.Unlock()
	if r.started.Load() {
		return
	}
	freezeRouterGraph(r, make(map[*Router[C]]bool))
}

func freezeRouterGraph[C Context](r *Router[C], seen map[*Router[C]]bool) {
	if seen[r] {
		return
	}
	seen[r] = true
	r.root.started.Store(true)
	for _, child := range r.children {
		freezeRouterGraph(child, seen)
	}
}

// compile turns the scope tree into the non-route half of a table: the fallback
// chains, the scope list and the host order. Routes never come through here;
// installInto puts those in the trie.
func (r *Router[C]) compile(eng *engine[C]) {
	eng.scopes, eng.errScopes = nil, nil
	if eng.hostSet != nil {
		for _, e := range eng.hostSet.all {
			e.mws, e.haveMWs = nil, false
			e.notFoundChain, e.notAllowedChain, e.optionsChain = nil, nil, nil
			e.errHandler = nil
		}
	}
	// A miss and a wrong method are errors like any other; only the error
	// handler is replaceable. The chains exist so a scope's middleware still
	// runs on the way to them.
	rootNotFound := HandlerFunc[C](defaultNotFound[C])
	rootNotAllowed := HandlerFunc[C](defaultMethodNotAllowed[C])
	rootErrHandler := ErrorHandlerFunc[C](DefaultErrorHandler[C])
	if r.errHandler != nil {
		rootErrHandler = r.errHandler
	}
	eng.notFoundChain = chain(rootNotFound, r.mws)
	eng.notAllowedChain = chain(rootNotAllowed, r.mws)
	eng.optionsChain = chain(autoOptions[C], r.mws)
	open := make(map[*Router[C]]bool)

	var pending []pendingScope[C]

	var walk func(rt *Router[C], prefix scopePattern, mws []Middleware[C], host *hostEntry[C], depth int, inherited scopeFallbacks[C])
	walk = func(rt *Router[C], prefix scopePattern, mws []Middleware[C], host *hostEntry[C], depth int, inherited scopeFallbacks[C]) {
		if open[rt] {
			panic("router: a router is mounted inside itself")
		}
		open[rt] = true
		defer delete(open, rt)

		p := prefix.join(rt)
		m := concatMiddleware(mws, rt.mws)

		rounds := max(len(rt.hosts), 1)
		for i := range rounds {
			e := host
			if len(rt.hosts) > 0 {
				if host != nil {
					panic("router: a host scope cannot sit inside another host scope")
				}
				e = eng.mustHostEntry(rt.hosts[i])
				if !e.haveMWs {
					e.mws, e.haveMWs = m, true
				}
			}

			own := inherited
			switch {
			// Only the router itself and a host scope own the root fallbacks;
			// mustOwnFallbacks rejects the rest at the setter.
			case rt == r || len(rt.hosts) > 0:
				if e == nil {
					if rt.errHandler != nil {
						rootErrHandler = rt.errHandler
					}
				} else if rt.errHandler != nil {
					e.errHandler = rt.errHandler
				}
			default:
				sets := own.take(rt)
				if normalizePattern(rt.prefix) != "/" || sets {
					pending = append(pending, pendingScope[C]{prefix: p, host: e, mws: m, depth: depth, fb: own})
				}
			}

			for _, ch := range rt.children {
				walk(ch, p, m, e, depth+1, own)
			}
		}
	}
	walk(r, scopePattern{}, nil, nil, 0, scopeFallbacks[C]{})

	if len(r.preMws) > 0 {
		r.preChain = chain(r.preTerminal, r.preMws)
	}
	if eng.hostSet != nil {
		for _, e := range eng.hostSet.all {
			if e.optionsChain == nil {
				e.optionsChain = chain(autoOptions[C], e.mws)
			}
			if e.notFoundChain == nil {
				e.notFoundChain = chain(rootNotFound, e.mws)
			}
			if e.notAllowedChain == nil {
				e.notAllowedChain = chain(rootNotAllowed, e.mws)
			}
		}
		slices.SortStableFunc(eng.hostSet.pats, func(a, b *hostEntry[C]) int {
			return lessSpecific(&a.hostSpec, &b.hostSpec)
		})
	}
	for _, ps := range pending {
		s := &scopeFallback[C]{
			prefix: ps.prefix.text, rec: &routeRecord{pattern: ps.prefix.text}, names: ps.prefix.names, pattern: ps.prefix.segs,
			hostIdx: -1, depth: ps.depth, errorIdx: -1,
		}
		if ps.host != nil {
			s.hostIdx = ps.host.idx
		}
		s.notFoundChain = chain(rootNotFound, ps.mws)
		s.notAllowedChain = chain(rootNotAllowed, ps.mws)
		s.optionsChain = chain(autoOptions[C], ps.mws)
		s.errHandler = ps.fb.errHandler
		eng.scopes = append(eng.scopes, s)
	}
	slices.SortStableFunc(eng.scopes, func(a, b *scopeFallback[C]) int {
		for i := range min(len(a.pattern), len(b.pattern)) {
			as := segmentSpecificity(a.pattern[i].kind)
			bs := segmentSpecificity(b.pattern[i].kind)
			if as != bs {
				return bs - as
			}
		}
		if len(a.pattern) != len(b.pattern) {
			return len(b.pattern) - len(a.pattern)
		}
		if a.depth != b.depth {
			return b.depth - a.depth
		}
		return strings.Compare(a.prefix, b.prefix)
	})
	for _, s := range eng.scopes {
		if s.errHandler != nil {
			s.errorIdx = int32(len(eng.errScopes))
			eng.errScopes = append(eng.errScopes, s)
		}
	}
	eng.rootErrorHandler = rootErrHandler
}

type pendingScope[C Context] struct {
	host   *hostEntry[C]
	mws    []Middleware[C]
	fb     scopeFallbacks[C]
	prefix scopePattern
	depth  int
}

// scopePattern is the joined prefix of a scope, as text and parsed. Each piece
// is parsed with the classes of the scope that wrote it; see parseScoped.
type scopePattern struct {
	text  string
	segs  []segment
	names []string
}

func (p scopePattern) join[C Context](rt *Router[C]) scopePattern {
	//nolint:errcheck // newChild rejected a bad prefix already.
	segs, names, _ := parsePattern(rt.prefix, rt.class)
	return scopePattern{
		text:  joinPattern(p.text, rt.prefix),
		segs:  slices.Concat(p.segs, segs),
		names: slices.Concat(p.names, names),
	}
}

type scopeFallbacks[C Context] struct {
	errHandler ErrorHandlerFunc[C]
}

func (f *scopeFallbacks[C]) take(rt *Router[C]) bool {
	if rt.errHandler == nil {
		return false
	}
	f.errHandler = rt.errHandler
	return true
}
