package router

import (
	"slices"
)

// scopePrefix joins the prefixes of every scope between the root and this one.
func (r *Router[C]) scopePrefix() string {
	prefix := ""
	for _, s := range slices.Backward(r.lineage()) {
		prefix = joinPattern(prefix, s.prefix)
	}
	return prefix
}

// parseScoped parses pattern joined to the prefixes of the scopes around this
// one. Each piece resolves its classes through the scope that wrote it, so a
// router mounted here keeps its own classes for its patterns and leaves the
// prefix of the parent to the classes of the parent.
func (r *Router[C]) parseScoped(pattern string) ([]segment, []string, error) {
	// The whole pattern first, for the errors that span pieces: a name used
	// twice, or a catch-all that is not last.
	if _, _, err := parsePattern(joinPattern(r.scopePrefix(), pattern), r.class); err != nil {
		return nil, nil, err
	}
	var (
		segs  []segment
		names []string
	)
	for _, s := range slices.Backward(r.lineage()) {
		ps, pn, err := parsePattern(s.prefix, s.class)
		if err != nil {
			return nil, nil, err
		}
		segs, names = append(segs, ps...), append(names, pn...)
	}
	ps, pn, err := parsePattern(pattern, r.class)
	if err != nil {
		return nil, nil, err
	}
	return append(segs, ps...), append(names, pn...), nil
}

// scopeMeta joins the values of every Meta scope between the root and this
// one, outermost first.
func (r *Router[C]) scopeMeta() []any {
	var meta []any
	for _, s := range slices.Backward(r.lineage()) {
		meta = append(meta, s.meta...)
	}
	return meta
}

func (r *Router[C]) scopeMiddleware() []Middleware[C] {
	var mws []Middleware[C]
	for _, s := range slices.Backward(r.lineage()) {
		mws = concatMiddleware(mws, s.mws)
	}
	return mws
}

// lineage lists this scope and its owners, nearest first.
func (r *Router[C]) lineage() []*Router[C] {
	var out []*Router[C]
	for s := r; s != nil; s = s.owner {
		out = append(out, s)
	}
	return out
}

// top is the router that owns the trie this scope registers into. It is the
// root until Mount hands the scope to a parent, and the parent's root after.
func (r *Router[C]) top() *Router[C] {
	s := r
	for s.owner != nil {
		s = s.owner
	}
	return s
}

// hostEntriesIn returns the entries of the nearest host scope, or none when the
// route answers on every host.
func (r *Router[C]) hostEntriesIn(eng *engine[C]) []*hostEntry[C] {
	for s := r; s != nil; s = s.owner {
		if len(s.hosts) == 0 {
			continue
		}
		out := make([]*hostEntry[C], 0, len(s.hosts))
		for _, spec := range s.hosts {
			e, err := eng.hostEntry(spec)
			if err != nil {
				panic(err.Error())
			}
			out = append(out, e)
		}
		return out
	}
	return nil
}

// Use adds middleware to this scope. It runs for every route the scope
// registers after the call, outermost first, and it does not reach a route
// registered before it.
//
// The middleware of the root, of a host scope and of a scope with a prefix also
// wraps the 404, the 405 and the automatic OPTIONS answer for the paths under
// it, with the host and prefix parameters bound, so it can answer a path that
// has no route. A [Router.Group] or [Router.With] scope adds no such answer of
// its own: its middleware reaches a 404 only through a scope with a prefix
// opened inside it.
//
// Use panics on a nil middleware, on a scope that already holds routes, on a
// mounted router, or after the router started serving.
func (r *Router[C]) Use(mws ...Middleware[C]) {
	validateMiddleware(mws)
	defer r.guard("add middleware")()
	if r.hasRoutes {
		panic("router: Use must come before the routes of a scope; open a Group for later middleware")
	}
	r.mws = append(r.mws, mws...)
	r.settingChanged()
}

func validateMiddleware[C Context](mws []Middleware[C]) {
	for _, mw := range mws {
		if mw == nil {
			panic("router: middleware must not be nil")
		}
	}
}

func (r *Router[C]) newChild(prefix string, mws []Middleware[C], meta []any) *Router[C] {
	validateMiddleware(mws)
	defer r.guard("open a scope")()
	if _, _, err := parsePattern(prefix, r.class); err != nil {
		panic(err.Error())
	}
	c := &Router[C]{root: r.root, owner: r, prefix: prefix, mws: mws, meta: meta, inHost: r.inHost || len(r.hosts) > 0}
	r.children = append(r.children, c)
	if normalizePattern(prefix) != "/" {
		c.settingChanged()
	}
	return c
}

// Group opens a scope with no prefix, for middleware that covers some routes
// and not others. fn registers into the scope, which Group also returns. Its
// middleware does not wrap the 404 and 405 answers; see [Router.Use].
func (r *Router[C]) Group(fn func(g *Router[C])) *Router[C] {
	var c *Router[C]
	r.inOneScope(func() {
		c = r.newChild("", nil, nil)
		if fn != nil {
			fn(c)
		}
	})
	return c
}

// Route opens a scope under prefix. The patterns that fn registers are
// relative to it, and the scope carries the middleware of its parent. Its
// middleware also wraps the 404 and 405 answers under prefix; see [Router.Use].
func (r *Router[C]) Route(prefix string, fn func(g *Router[C])) *Router[C] {
	var c *Router[C]
	r.inOneScope(func() {
		c = r.newChild(prefix, nil, nil)
		if fn != nil {
			fn(c)
		}
	})
	return c
}

// With opens a scope that carries mws on top of the middleware of its parent.
// Unlike [Router.Use] it registers nothing itself, so it suits a single route:
// r.With(auth).GET("/me", me).
func (r *Router[C]) With(mws ...Middleware[C]) *Router[C] {
	return r.newChild("", slices.Clone(mws), nil)
}

// Meta opens a scope whose routes carry v on top of the values of the scopes
// around it. A Route, Group, With or Host opened inside it, and a router
// mounted into it, inherit the values. [Router.Routes] reports them, which lets
// a route table drive a permission list or an OpenAPI document, and
// [Base.RouteMeta] and [MetaAs] read them while the route answers. Values are
// shared by every request, so keep them immutable. A router mounted with its
// own context type (MountRouter, HostRouter) does not see them.
//
// Meta panics on no value, on a nil value, or after the router started serving.
func (r *Router[C]) Meta(v ...any) *Router[C] {
	if len(v) == 0 {
		panic("router: Meta needs at least one value")
	}
	if slices.Contains(v, nil) {
		panic("router: Meta got a nil value; register the route outside the Meta scope instead")
	}
	return r.newChild("", nil, slices.Clone(v))
}
