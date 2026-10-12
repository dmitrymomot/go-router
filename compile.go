package router

import (
	"slices"
)

// settingChanged rebuilds the derived state so the router is ready to serve the
// moment the setter returns. A change inside a scope that waits for its first
// compile needs no more than that compile, which reads it.
func (r *Router[C]) settingChanged() {
	root := r.top()
	if !r.inFreshScope() {
		root.fullCompile = true
	}
	if root.refreshDepth == 0 {
		root.refresh()
	}
}

// scopeOpened queues the new scope r for a compile of its own subtree, so
// opening the thousandth scope does not recompile the 999 before it.
func (r *Router[C]) scopeOpened() {
	root := r.top()
	if !r.inFreshScope() {
		r.fresh = true
		root.freshScopes = append(root.freshScopes, r)
	}
	if root.refreshDepth == 0 {
		root.refresh()
	}
}

// inFreshScope reports whether r or a scope around it waits for its first
// compile.
func (r *Router[C]) inFreshScope() bool {
	for s := r; s != nil; s = s.owner {
		if s.fresh {
			return true
		}
	}
	return false
}

// refresh brings the live table up to date in place: it compiles each scope
// opened since the last refresh as a subtree of its own, and the whole graph
// when anything else changed or a new subtree cannot go in on its own.
func (r *Router[C]) refresh() {
	fresh, full := r.freshScopes, r.fullCompile
	r.freshScopes, r.fullCompile = nil, false
	for _, s := range fresh {
		s.fresh = false
	}
	for _, s := range fresh {
		if full {
			break
		}
		full = !r.compileScope(s)
	}
	if full {
		r.compile()
	}
}

// inOneScope holds the rebuild until the outermost scope callback returns, so
// a nested Route, Group or Hosts joins the compile of the scope around it. The
// router is still ready to serve the moment that outermost call returns.
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
// chains, the scope index, the host order and the error handler of every
// scope. Routes never come through here; install puts those in the trie, and
// compile only rewrites the error handler index they point at.
func (r *Router[C]) compile() {
	eng := r.eng
	eng.scopeIdx = nil
	if eng.hostSet != nil {
		for _, e := range eng.hostSet.all {
			e.mws = nil
			e.notFoundChain, e.notAllowedChain, e.optionsChain = nil, nil, nil
			e.errIdx, e.scope, e.handler = 0, nil, nil
			e.scopeIdx, e.compiled = nil, false
		}
	}
	// A miss and a wrong method are errors like any other; only the error
	// handler is replaceable. The chains exist so a scope's middleware still
	// runs on the way to them.
	eng.notFoundChain = chain(defaultNotFound[C], r.mws)
	eng.notAllowedChain = chain(defaultMethodNotAllowed[C], r.mws)
	eng.optionsChain = chain(autoOptions[C], r.mws)

	// The table is built fresh: a request never reads it before the router
	// serves, and no setter runs after that.
	rootErr := r.errHandler
	if rootErr == nil {
		rootErr = TextErrorHandler[C](false)
	}
	eng.errHandlers = []ErrorHandlerFunc[C]{rootErr}
	eng.owners = map[*Router[C]]int32{r: 0}
	eng.errResolved = map[errKey[C]]int32{}

	cp := &compiler[C]{root: r, eng: eng, open: map[*Router[C]]bool{}}
	cp.walk(r, walkCtx[C]{inner: -1})
	cp.resolve()

	if len(r.preMws) > 0 {
		r.preChain = chain(r.preTerminal, r.preMws)
	}
	if eng.hostSet != nil {
		for _, e := range eng.hostSet.all {
			cp.finishHost(e)
		}
		slices.SortStableFunc(eng.hostSet.pats, func(a, b *hostEntry[C]) int {
			return lessSpecific(&a.hostSpec, &b.hostSpec)
		})
	}
	cp.addScopes(false)
}

// compileScope adds the subtree of s, a scope opened since the last compile,
// to the table without walking the rest of the graph. It reports false when
// the subtree cannot go in on its own and the graph needs a full compile: a
// host scope inside it names a host that a compiled scope uses already, or a
// prefix in it repeats one that a compiled scope of the same depth and host
// holds. The order of the full walk decides either one.
func (r *Router[C]) compileScope(s *Router[C]) bool {
	eng := r.eng
	if eng.owners == nil || s.top() != r || !newHostsOnly(eng, s) {
		return false
	}
	cp := &compiler[C]{root: r, eng: eng, open: map[*Router[C]]bool{}}
	// Replay the walk from the root down to the owner of s, for the context
	// that the full walk gives s on every host round above it.
	ctxs := []walkCtx[C]{{inner: -1}}
	for _, a := range slices.Backward(s.lineage()[1:]) {
		next := make([]walkCtx[C], 0, len(ctxs))
		for _, ctx := range ctxs {
			for i := range max(len(a.hosts), 1) {
				next = append(next, cp.enter(a, ctx, i, false))
			}
		}
		ctxs = next
	}
	for _, ctx := range ctxs {
		cp.walk(s, ctx)
	}
	cp.resolve()
	if !cp.addScopes(true) {
		return false
	}
	if hs := eng.hostSet; hs != nil {
		for _, e := range hs.all {
			if e.compiled {
				continue
			}
			cp.finishHost(e)
			// The other patterns stay sorted from the last compile, so the new
			// one goes after every pattern that does not sort after it, where
			// the stable sort of a full compile puts it.
			if i := slices.Index(hs.pats, e); i >= 0 {
				hs.pats = slices.Delete(hs.pats, i, i+1)
				at, _ := slices.BinarySearchFunc(hs.pats, e, func(have, want *hostEntry[C]) int {
					if c := lessSpecific(&have.hostSpec, &want.hostSpec); c != 0 {
						return c
					}
					return -1
				})
				hs.pats = slices.Insert(hs.pats, at, e)
			}
		}
	}
	return true
}

// newHostsOnly reports whether every host scope in the subtree of s names
// hosts that no compiled scope uses yet.
func newHostsOnly[C Context](eng *engine[C], s *Router[C]) bool {
	for _, spec := range s.hosts {
		e, err := eng.hostEntry(spec)
		if err != nil || e.compiled {
			return false
		}
	}
	for _, ch := range s.children {
		if !newHostsOnly(eng, ch) {
			return false
		}
	}
	return true
}

// compiler holds one walk of the scope graph, whole or one subtree of it.
type compiler[C Context] struct {
	root    *Router[C]
	eng     *engine[C]
	open    map[*Router[C]]bool
	choices []choice[C]
	pending []pendingScope[C]
}

// A choice is where a scope stands on one tree: inner is the nearest handler
// inside its host scope, -1 for none, and outer the one around that host
// scope. The host's own handler sits between them, and it is known only once
// every scope of the host was seen.
type choice[C Context] struct {
	key          errKey[C]
	inner, outer int32
}

// walkCtx is what the walk carries from a scope into its children.
type walkCtx[C Context] struct {
	host         *hostEntry[C]
	mount        *mountPath[C]
	mws          []Middleware[C]
	prefix       scopePattern
	depth        int
	inner, outer int32
}

func (cp *compiler[C]) walk(rt *Router[C], ctx walkCtx[C]) {
	if cp.open[rt] {
		panic("router: a router is mounted inside itself")
	}
	cp.open[rt] = true
	defer delete(cp.open, rt)
	for i := range max(len(rt.hosts), 1) {
		inner := cp.enter(rt, ctx, i, true)
		for _, ch := range rt.children {
			cp.walk(ch, inner)
		}
	}
}

// enter steps into rt on host round i and returns the context of its
// children. With record set it also notes the choice of rt, its fallback and
// its claim on its host; the replay above a new subtree needs the context
// alone.
func (cp *compiler[C]) enter(rt *Router[C], ctx walkCtx[C], i int, record bool) walkCtx[C] {
	eng := cp.eng
	p := ctx.prefix.join(rt)
	m := concatMiddleware(ctx.mws, rt.mws)
	atRoot := normalizePattern(p.text) == "/"
	// The scopes inside a Mount shim sit one mount deeper.
	below := ctx.mount
	if rt.mounted != nil {
		below = eng.mountIn(ctx.mount, rt)
	}

	e, in, out := ctx.host, ctx.inner, ctx.outer
	if len(rt.hosts) > 0 {
		if ctx.host != nil {
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
		// A host scope at the root answers the paths of its host that no
		// prefix covers: the one with a handler, else the first.
		if record && atRoot && e.scope == nil {
			e.scope, e.mws, e.errIdx = rt, m, out
		}
	}
	if rt != cp.root && rt.errHandler != nil {
		in = cp.ownerIdx(rt)
		if record && len(rt.hosts) > 0 {
			if e.handler != nil && e.handler != rt {
				panic("router: the host " + e.pattern + " already has an error handler, set by another Host scope")
			}
			e.handler = rt
			if atRoot {
				e.scope, e.mws, e.errIdx = rt, m, in
			}
		}
	}
	if record {
		key := errKey[C]{rt, e, ctx.mount}
		cp.choices = append(cp.choices, choice[C]{key: key, inner: in, outer: out})

		// A scope with a prefix answers the 404s under it, and so does a host
		// scope under one, on its host.
		if rt != cp.root && (normalizePattern(rt.prefix) != "/" || len(rt.hosts) > 0 && !atRoot) {
			// A mount answers the 404s under its prefix as the router it holds
			// would, with that router's middleware.
			owner, pm := key, m
			if sub := rt.mounted; sub != nil {
				owner, pm = errKey[C]{sub, e, below}, concatMiddleware(m, sub.mws)
			}
			cp.pending = append(cp.pending, pendingScope[C]{prefix: p, host: e, mws: pm, depth: ctx.depth, owner: owner})
		}
	}
	return walkCtx[C]{prefix: p, mws: m, host: e, mount: below, depth: ctx.depth + 1, inner: in, outer: out}
}

// ownerIdx returns the index of the error handler of rt in the table, adding
// it on first sight.
func (cp *compiler[C]) ownerIdx(rt *Router[C]) int32 {
	eng := cp.eng
	idx, ok := eng.owners[rt]
	if !ok {
		idx = int32(len(eng.errHandlers))
		eng.owners[rt] = idx
		eng.errHandlers = append(eng.errHandlers, rt.errHandler)
	}
	return idx
}

// resolve settles the error handler of every scope the walk saw and writes it
// to the slot that its routes read.
func (cp *compiler[C]) resolve() {
	eng := cp.eng
	for _, c := range cp.choices {
		idx := c.outer
		switch {
		case c.inner >= 0:
			idx = c.inner
		case c.key.host != nil && c.key.host.handler != nil:
			idx = eng.owners[c.key.host.handler]
		}
		eng.errResolved[c.key] = idx
		if slot := eng.errSlots[c.key]; slot != nil {
			*slot = idx
		}
	}
}

// finishHost builds the answers of e outside every prefix.
func (cp *compiler[C]) finishHost(e *hostEntry[C]) {
	if e.scope == nil {
		// No host scope sits at the root: the router answers.
		e.mws = cp.root.mws
	}
	e.optionsChain = chain(autoOptions[C], e.mws)
	e.notFoundChain = chain(defaultNotFound[C], e.mws)
	e.notAllowedChain = chain(defaultMethodNotAllowed[C], e.mws)
	e.compiled = true
}

// addScopes puts the fallback of every scope the walk saw in the index of its
// host. With unique set it stops, reporting false, at a prefix that the index
// already holds for the same depth and host.
func (cp *compiler[C]) addScopes(unique bool) bool {
	eng := cp.eng
	for _, ps := range cp.pending {
		s := &scopeFallback[C]{
			prefix: ps.prefix.text, rec: &routeRecord{pattern: ps.prefix.text}, names: ps.prefix.names, pattern: ps.prefix.segs,
			depth: ps.depth, errIdx: eng.errResolved[ps.owner],
		}
		idx := &eng.scopeIdx
		if ps.host != nil {
			idx = &ps.host.scopeIdx
		}
		s.notFoundChain = chain(defaultNotFound[C], ps.mws)
		s.notAllowedChain = chain(defaultMethodNotAllowed[C], ps.mws)
		s.optionsChain = chain(autoOptions[C], ps.mws)
		if *idx == nil {
			*idx = new(scopeNode[C])
		}
		if !(*idx).add(s, unique) {
			return false
		}
	}
	return true
}

// errSlot returns the owner index that the routes of r read on the tree of
// host, nil for the tree of every host. A scope that compile has not seen yet,
// such as a With scope, takes the index of the nearest owner that compile saw,
// so the route answers correctly the moment it is registered. Only a scope
// with a handler or a host of its own in between takes a compile now; inside a
// Route, Group or Hosts callback the compile at the end of the outermost one
// fills it.
func (r *Router[C]) errSlot(eng *engine[C], host *hostEntry[C]) *int32 {
	lineage := r.lineage()
	mounts := eng.mountPathsOf(lineage)
	key := errKey[C]{r, host, mounts[0]}
	if slot := eng.errSlots[key]; slot != nil {
		return slot
	}
	slot := new(int32)
	eng.errSlots[key] = slot
	if idx, ok := eng.inheritedOwner(lineage, mounts, host); ok {
		*slot = idx
	} else {
		r.settingChanged()
	}
	return slot
}

// mountPathsOf returns the mount path of each scope of lineage, which lists a
// scope and its owners nearest first.
func (e *engine[C]) mountPathsOf(lineage []*Router[C]) []*mountPath[C] {
	out := make([]*mountPath[C], len(lineage))
	var p *mountPath[C]
	for i, s := range slices.Backward(lineage) {
		out[i] = p
		if s.mounted != nil {
			p = e.mountIn(p, s)
		}
	}
	return out
}

// inheritedOwner resolves a scope that compile has not seen. A scope with no
// handler and no host of its own resolves as its owner does on the same tree,
// so the first resolved scope up the lineage answers for it.
func (e *engine[C]) inheritedOwner(lineage []*Router[C], mounts []*mountPath[C], host *hostEntry[C]) (int32, bool) {
	for i, s := range lineage {
		if idx, ok := e.errResolved[errKey[C]{s, host, mounts[i]}]; ok {
			return idx, true
		}
		if s.errHandler != nil || len(s.hosts) > 0 {
			return 0, false
		}
	}
	return 0, false
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
