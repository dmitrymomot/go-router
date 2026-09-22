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
// chains, the scope list, the host order and the error handler of every scope.
// Routes never come through here; install puts those in the trie, and compile
// only rewrites the error handler index they point at.
func (r *Router[C]) compile(eng *engine[C]) {
	eng.scopes = nil
	if eng.hostSet != nil {
		for _, e := range eng.hostSet.all {
			e.mws, e.haveMWs = nil, false
			e.notFoundChain, e.notAllowedChain, e.optionsChain = nil, nil, nil
			e.errIdx, e.scope = 0, nil
		}
	}
	// A miss and a wrong method are errors like any other; only the error
	// handler is replaceable. The chains exist so a scope's middleware still
	// runs on the way to them.
	rootNotFound := HandlerFunc[C](defaultNotFound[C])
	rootNotAllowed := HandlerFunc[C](defaultMethodNotAllowed[C])
	eng.notFoundChain = chain(rootNotFound, r.mws)
	eng.notAllowedChain = chain(rootNotAllowed, r.mws)
	eng.optionsChain = chain(autoOptions[C], r.mws)

	// The table is built fresh: a request never reads it before the router
	// serves, and no setter runs after that.
	rootErr := r.errHandler
	if rootErr == nil {
		rootErr = TextErrorHandler[C](false)
	}
	eng.errHandlers = []ErrorHandlerFunc[C]{rootErr}
	owners := map[*Router[C]]int32{r: 0}
	ownerIdx := func(rt *Router[C]) int32 {
		idx, ok := owners[rt]
		if !ok {
			idx = int32(len(eng.errHandlers))
			owners[rt] = idx
			eng.errHandlers = append(eng.errHandlers, rt.errHandler)
		}
		return idx
	}

	// A choice is where a scope stands on one tree: inner is the nearest
	// handler inside its host scope, -1 for none, and outer the one around that
	// host scope. The host's own handler sits between them, and it is known only
	// once every scope of the host was seen.
	type choice struct {
		key          errKey[C]
		inner, outer int32
	}
	var choices []choice
	open := make(map[*Router[C]]bool)
	var pending []pendingScope[C]

	var walk func(rt *Router[C], prefix scopePattern, mws []Middleware[C], host *hostEntry[C], depth int, inner, outer int32)
	walk = func(rt *Router[C], prefix scopePattern, mws []Middleware[C], host *hostEntry[C], depth int, inner, outer int32) {
		if open[rt] {
			panic("router: a router is mounted inside itself")
		}
		open[rt] = true
		defer delete(open, rt)

		p := prefix.join(rt)
		m := concatMiddleware(mws, rt.mws)

		rounds := max(len(rt.hosts), 1)
		for i := range rounds {
			e, in, out := host, inner, outer
			if len(rt.hosts) > 0 {
				if host != nil {
					panic("router: a host scope cannot sit inside another host scope")
				}
				var err error
				if e, err = eng.hostEntry(rt.hosts[i]); err != nil {
					panic(err.Error())
				}
				if in >= 0 {
					out = in
				}
				in = -1
				if !e.haveMWs {
					e.mws, e.haveMWs = m, true
				}
				if e.scope == nil {
					e.scope = rt
				}
			}
			if rt != r && rt.errHandler != nil {
				in = ownerIdx(rt)
				if len(rt.hosts) > 0 {
					if e.scope != rt && e.scope.errHandler != nil {
						panic("router: the host " + e.pattern + " already has an error handler, set by another Host scope")
					}
					// The scope that owns the host's errors also wraps its 404s.
					e.scope, e.mws = rt, m
				}
			}
			choices = append(choices, choice{key: errKey[C]{rt, e}, inner: in, outer: out})

			if rt != r && normalizePattern(rt.prefix) != "/" {
				// A mount answers the 404s under its prefix as the router it
				// holds would, with that router's middleware.
				owner, pm := rt, m
				if sub := rt.mounted; sub != nil {
					owner, pm = sub, concatMiddleware(m, sub.mws)
				}
				pending = append(pending, pendingScope[C]{prefix: p, host: e, mws: pm, depth: depth, owner: errKey[C]{owner, e}})
			}

			for _, ch := range rt.children {
				walk(ch, p, m, e, depth+1, in, out)
			}
		}
	}
	walk(r, scopePattern{}, nil, nil, 0, -1, 0)

	resolved := make(map[errKey[C]]int32, len(choices))
	for _, c := range choices {
		idx := c.outer
		switch {
		case c.inner >= 0:
			idx = c.inner
		case c.key.host != nil && c.key.host.scope.errHandler != nil:
			idx = owners[c.key.host.scope]
		}
		resolved[c.key] = idx
		if slot := eng.errSlots[c.key]; slot != nil {
			*slot = idx
		}
	}
	eng.errResolved = resolved

	if len(r.preMws) > 0 {
		r.preChain = chain(r.preTerminal, r.preMws)
	}
	if eng.hostSet != nil {
		for _, e := range eng.hostSet.all {
			if e.scope != nil {
				e.errIdx = resolved[errKey[C]{e.scope, e}]
			}
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
			hostIdx: -1, depth: ps.depth, errIdx: resolved[ps.owner],
		}
		if ps.host != nil {
			s.hostIdx = ps.host.idx
		}
		s.notFoundChain = chain(rootNotFound, ps.mws)
		s.notAllowedChain = chain(rootNotAllowed, ps.mws)
		s.optionsChain = chain(autoOptions[C], ps.mws)
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
}

// errSlot returns the owner index that the routes of r read on the tree of
// host, nil for the tree of every host. A scope that compile has not seen yet,
// such as a With scope, takes a compile now, so the route answers correctly the
// moment it is registered; inside a Route, Group or Hosts callback the compile
// at the end of the outermost one fills it.
func (r *Router[C]) errSlot(eng *engine[C], host *hostEntry[C]) *int32 {
	key := errKey[C]{r, host}
	if slot := eng.errSlots[key]; slot != nil {
		return slot
	}
	slot := new(int32)
	eng.errSlots[key] = slot
	if idx, ok := eng.errResolved[key]; ok {
		*slot = idx
	} else {
		r.settingChanged()
	}
	return slot
}

type pendingScope[C Context] struct {
	host   *hostEntry[C]
	mws    []Middleware[C]
	owner  errKey[C]
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
