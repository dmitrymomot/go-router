package router

import (
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// Route describes one registered route. [Router.Routes] returns them.
type Route struct {
	Method  string
	Pattern string

	// Host is the host pattern of the scope that registered the route. It is
	// empty for a route that answers every host. It comes last, so that an
	// unkeyed literal of the two fields above still compiles.
	Host string
}

// stdMethods are the methods that [Router.Any] and [Router.MountHandler]
// register.
var stdMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodConnect,
	http.MethodOptions, http.MethodTrace,
}

// registration is one route as the application declared it, before the router
// resolves prefixes and middleware.
type registration[C Context] struct {
	method  string
	pattern string
	handler HandlerFunc[C]
	mws     []Middleware[C]
}

// Router routes requests to handlers of the shape func(C) error, where C is
// the application context type.
//
// A Router is either a root, which [New] returns, or a scope that [Router.Group],
// [Router.Route] or [Router.With] returns. A scope adds a path prefix or
// middleware to the routes that it registers, and shares the route trie of its
// root.
//
// Register every route before the first request. The router compiles the trie
// once, on the first call to ServeHTTP, and panics if a route arrives after
// that.
type Router[C Context] struct {
	root   *Router[C]
	prefix string
	mws    []Middleware[C]

	regs      []registration[C]
	children  []*Router[C]
	hasRoutes bool

	// hosts holds the host patterns that [Router.Host] bound to this scope. It
	// is nil for a scope that answers every host.
	hosts []hostSpec

	// inHost reports that a host scope encloses this one, which is what lets
	// Hosts refuse a nested host scope at registration.
	inHost bool

	// The fallbacks of this scope. Each one is nil until the scope sets it.
	// The root carries the defaults, and a host scope that leaves one unset
	// falls back to them.
	notFound         HandlerFunc[C]
	methodNotAllowed HandlerFunc[C]
	errHandler       ErrorHandlerFunc[C]

	// The fields below belong to the root only.
	newCtx          func(http.ResponseWriter, *http.Request) C
	pool            *sync.Pool
	reset           func(C)
	once            sync.Once
	started         atomic.Bool
	tree            *node[C]
	hostSet         *hostSet[C]
	routes          []Route
	notFoundChain   HandlerFunc[C]
	notAllowedChain HandlerFunc[C]
	optionsChain    HandlerFunc[C]
	autoOptions     bool
	redirectSlash   bool

	// anyHostRoutes reports whether a route outside every host scope exists.
	// Without one the router skips the second trie walk on a miss.
	anyHostRoutes bool

	maxBody  int64
	jsonOpts []json.Options
}

// New returns a root router. newContext builds the application context for one
// request; the router fills the embedded [Base] afterwards, so the factory only
// has to supply the application fields:
//
//	r := router.New(func(w http.ResponseWriter, req *http.Request) *app.Context {
//		return &app.Context{DB: db, Log: log}
//	})
//
// New panics when newContext is nil. newContext itself must never return a
// nil context, because the router writes the request state into the embedded
// [Base].
func New[C Context](newContext func(http.ResponseWriter, *http.Request) C) *Router[C] {
	if newContext == nil {
		panic("router: New needs a context factory")
	}
	r := &Router[C]{
		newCtx:           newContext,
		notFound:         defaultNotFound[C],
		methodNotAllowed: defaultMethodNotAllowed[C],
		errHandler:       DefaultErrorHandler[C],
		autoOptions:      true,
		maxBody:          DefaultMaxBodyBytes,
	}
	r.root = r
	return r
}

// NewPooled returns a root router that reuses application contexts instead of
// allocating one per request. It removes the single allocation that a request
// otherwise costs.
//
// newContext takes no request, because a pooled context outlives the request
// that first created it: read anything request specific in a middleware or a
// handler, never in the factory.
//
// reset must clear every field that a handler or a middleware writes. The
// router clears the embedded [Base] itself. A field that reset forgets carries
// the data of one request into the next one, which is a data leak between
// users, so write reset as an explicit assignment of every field:
//
//	r := router.NewPooled(
//		func() *app.Context { return &app.Context{DB: db} },
//		func(c *app.Context) { c.User = nil; c.Tenant = "" },
//	)
//
// Never keep a context, its request or its response writer alive after the
// handler returns. A goroutine that outlives the request would then read and
// write the context of an unrelated request. Copy what the goroutine needs
// before it starts.
//
// The router does not pool a context whose request panicked, because its state
// is unknown at that point.
func NewPooled[C Context](newContext func() C, reset func(c C)) *Router[C] {
	if newContext == nil {
		panic("router: NewPooled needs a context factory")
	}
	if reset == nil {
		panic("router: NewPooled needs a reset function")
	}
	r := New(func(http.ResponseWriter, *http.Request) C { return newContext() })
	r.pool = &sync.Pool{New: func() any { return newContext() }}
	r.reset = reset
	return r
}

func defaultNotFound[C Context](C) error         { return ErrNotFound }
func defaultMethodNotAllowed[C Context](C) error { return ErrMethodNotAllowed }

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// Handle registers a handler for one method and pattern. The optional
// middleware applies to this route only, inside the middleware of the scope.
func (r *Router[C]) Handle(method, pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	if r.root.started.Load() {
		panic("router: cannot register " + method + " " + pattern + " after the router started serving")
	}
	if method == "" {
		panic("router: Handle needs a method")
	}
	if h == nil {
		panic("router: Handle needs a handler for " + method + " " + pattern)
	}
	r.hasRoutes = true
	r.regs = append(r.regs, registration[C]{
		method:  method,
		pattern: pattern,
		handler: h,
		mws:     slices.Clone(mws),
	})
}

// GET registers a handler for the GET method. A HEAD request falls back to it
// unless the pattern also has a HEAD handler.
func (r *Router[C]) GET(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodGet, pattern, h, mws...)
}

// HEAD registers a handler for the HEAD method.
func (r *Router[C]) HEAD(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodHead, pattern, h, mws...)
}

// POST registers a handler for the POST method.
func (r *Router[C]) POST(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodPost, pattern, h, mws...)
}

// PUT registers a handler for the PUT method.
func (r *Router[C]) PUT(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodPut, pattern, h, mws...)
}

// PATCH registers a handler for the PATCH method.
func (r *Router[C]) PATCH(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodPatch, pattern, h, mws...)
}

// DELETE registers a handler for the DELETE method.
func (r *Router[C]) DELETE(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodDelete, pattern, h, mws...)
}

// OPTIONS registers a handler for the OPTIONS method. It replaces the
// automatic answer that [Router.HandleOPTIONS] controls.
func (r *Router[C]) OPTIONS(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodOptions, pattern, h, mws...)
}

// Any registers a handler for every standard method.
func (r *Router[C]) Any(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	for _, m := range stdMethods {
		r.Handle(m, pattern, h, mws...)
	}
}

// Match registers a handler for each of the named methods.
func (r *Router[C]) Match(methods []string, pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	for _, m := range methods {
		r.Handle(m, pattern, h, mws...)
	}
}

// ---------------------------------------------------------------------------
// Scopes
// ---------------------------------------------------------------------------

// Use adds middleware to this scope. It applies to every route that the scope
// registers afterwards, and to every scope below it.
//
// Use panics after the scope registers its first route, because middleware that
// arrives later would silently skip the routes above it.
func (r *Router[C]) Use(mws ...Middleware[C]) {
	if r.hasRoutes {
		panic("router: Use must come before the routes of a scope; open a Group for later middleware")
	}
	if r.root.started.Load() {
		panic("router: cannot add middleware after the router started serving")
	}
	r.mws = append(r.mws, mws...)
}

// newChild adds a scope below r.
func (r *Router[C]) newChild(prefix string, mws []Middleware[C]) *Router[C] {
	c := &Router[C]{root: r.root, prefix: prefix, mws: mws, inHost: r.inHost || len(r.hosts) > 0}
	r.children = append(r.children, c)
	return c
}

// Group opens a scope at the same path prefix. Use it to apply middleware to
// part of the routes:
//
//	r.Group(func(g *router.Router[*app.Context]) {
//		g.Use(auth)
//		g.GET("/me", meHandler)
//	})
func (r *Router[C]) Group(fn func(g *Router[C])) *Router[C] {
	c := r.newChild("", nil)
	if fn != nil {
		fn(c)
	}
	return c
}

// Route opens a scope at a path prefix:
//
//	r.Route("/admin", func(g *router.Router[*app.Context]) {
//		g.Use(requireAdmin)
//		g.GET("/users", listUsers)   // GET /admin/users
//	})
func (r *Router[C]) Route(prefix string, fn func(g *Router[C])) *Router[C] {
	c := r.newChild(prefix, nil)
	if fn != nil {
		fn(c)
	}
	return c
}

// With returns a scope at the same path prefix that carries extra middleware:
//
//	r.With(rateLimit).POST("/login", login)
func (r *Router[C]) With(mws ...Middleware[C]) *Router[C] {
	return r.newChild("", slices.Clone(mws))
}

// ---------------------------------------------------------------------------
// Host scopes
// ---------------------------------------------------------------------------

// Host opens a scope whose routes answer one host, and returns it.
//
//	r.Host("api.example.com", func(h *router.Router[*app.Context]) {
//		h.Use(apiKey)
//		h.GET("/v1/users/{id}", getUser)
//	})
//
// The pattern is a host name whose labels may carry parameters, in the syntax
// that [Router.Hosts] documents. A route that no host scope registers answers
// every host, and the router falls back to it when the routes of the matched
// host do not answer the path.
func (r *Router[C]) Host(pattern string, fn func(h *Router[C])) *Router[C] {
	return r.Hosts([]string{pattern}, fn)
}

// Hosts opens one scope for several host patterns, which is what a tenant that
// may arrive on a subdomain or on a domain of its own needs:
//
//	r.Hosts([]string{"{tenant}.example.com", "*"}, func(h *router.Router[*app.Context]) {
//		h.Use(resolveTenant)          // c.Param("tenant"), or c.Host() otherwise
//		h.GET("/", dashboard)
//	})
//
// Pattern syntax:
//
//	example.com                    that host exactly
//	{tenant}.example.com           one label, readable as c.Param("tenant")
//	{tenant:[a-z0-9-]+}.ex.com     one label that the regular expression accepts
//	acme-{env}.example.com         part of a label
//	{sub...}.example.com           one or more leading labels, as one value
//	*.example.com                  one label, with no value kept
//	*                              any host
//
// The router matches the host without its port and without a trailing dot, and
// it lowercases both the pattern and the request host, so "example.com" also
// answers "Example.com:8080" in development. A port in a pattern is a
// registration error.
//
// A fixed host wins over a pattern, and among patterns the one with the most
// static labels wins, so "www.example.com" beats "{tenant}.example.com" and
// both beat "*". The scope owns its middleware, and [Router.NotFound],
// [Router.MethodNotAllowed] and [Router.ErrorHandler] inside it apply to this
// host alone; each falls back to the one of the root while it is unset.
//
// A host scope inside another host scope is a registration error.
func (r *Router[C]) Hosts(patterns []string, fn func(h *Router[C])) *Router[C] {
	if r.root.started.Load() {
		panic("router: cannot register a host scope after the router started serving")
	}
	if len(patterns) == 0 {
		panic("router: Hosts needs at least one pattern")
	}
	if r.inHost || len(r.hosts) > 0 {
		panic("router: a host scope cannot sit inside another host scope")
	}
	specs := make([]hostSpec, 0, len(patterns))
	for _, p := range patterns {
		spec, err := parseHostPattern(p)
		if err != nil {
			panic(err.Error())
		}
		specs = append(specs, spec)
	}
	c := r.newChild("", nil)
	c.hosts = specs
	if fn != nil {
		fn(c)
	}
	return c
}

// HostRouter gives a whole host to a router that uses a different context
// type. That router serves the request on its own, with its own context
// factory, middleware, error handler and fallbacks, and sees the path
// unchanged:
//
//	r.HostRouter("api.example.com", api.Router(db))   // *api.Context
//
// The middleware of r still runs in front of it, which is where a recover, a
// request id and a log line belong. A parameter of the host pattern does not
// cross the seam; read it in a middleware of r and pass it on.
func (r *Router[C]) HostRouter[D Context](pattern string, sub *Router[D]) {
	if sub == nil {
		panic("router: HostRouter needs a router")
	}
	r.HostHandler(pattern, sub)
}

// HostHandler gives a whole host to any [http.Handler].
//
//	r.HostHandler("docs.example.com", http.FileServerFS(docs))
func (r *Router[C]) HostHandler(pattern string, h http.Handler) {
	if h == nil {
		panic("router: HostHandler needs a handler")
	}
	r.Host(pattern, func(g *Router[C]) { g.MountHandler("/", h) })
}

// Mount attaches a router of the same context type at a path prefix. The
// routes of sub join the trie of the root, so a parameter in the prefix stays
// readable inside sub and matching costs no more than a flat route:
//
//	api := router.New(newCtx)
//	api.GET("/users/{id}", getUser)
//	r.Mount("/api/v1", api)          // GET /api/v1/users/{id}
//
// The middleware of r applies to the routes of sub. The context factory, the
// error handler and the fallbacks of sub are ignored; the root supplies them.
func (r *Router[C]) Mount(prefix string, sub *Router[C]) {
	if sub == nil {
		panic("router: Mount needs a router")
	}
	shim := r.newChild(prefix, nil)
	shim.children = append(shim.children, sub)
}

// MountRouter attaches a router that uses a different context type. The
// mounted router serves the request on its own, with its own context factory,
// middleware, error handler and fallbacks, and sees the path with the prefix
// removed.
//
//	admin := router.New(newAdminCtx)   // *admin.Context
//	r.MountRouter("/admin", admin)     // from a *app.Context router
func (r *Router[C]) MountRouter[D Context](prefix string, sub *Router[D]) {
	if sub == nil {
		panic("router: MountRouter needs a router")
	}
	r.MountHandler(prefix, sub)
}

// MountHandler attaches any [http.Handler] at a path prefix, in the way that
// [http.StripPrefix] does: the handler sees the path with the prefix removed.
//
//	r.MountHandler("/static", http.FileServerFS(assets))
func (r *Router[C]) MountHandler(prefix string, h http.Handler) {
	if h == nil {
		panic("router: MountHandler needs a handler")
	}
	prefix = normalizePattern(prefix)
	handler := func(c C) error {
		b := c.base()
		req := stripMountPrefix(b.req, b.rawTail, b.pathEscaped)
		b.SetRequest(req)
		h.ServeHTTP(b.res, req)
		return nil
	}
	r.Any(prefix, handler)
	r.Any(joinPattern(prefix, "/{"+mountParam+"...}"), handler)
}

// stripMountPrefix returns a shallow copy of the request whose path is the
// part that the mount pattern did not consume.
func stripMountPrefix(r *http.Request, tail string, escaped bool) *http.Request {
	r2 := new(http.Request)
	*r2 = *r
	u := new(url.URL)
	*u = *r.URL
	r2.URL = u

	if tail == "" {
		u.Path, u.RawPath = "/", ""
		return r2
	}
	rest := "/" + tail
	u.Path, u.RawPath = rest, ""
	if escaped {
		if unescaped, err := url.PathUnescape(rest); err == nil && unescaped != rest {
			u.Path, u.RawPath = unescaped, rest
		}
	}
	return r2
}

// ---------------------------------------------------------------------------
// Root settings
// ---------------------------------------------------------------------------

// mustNotBeServing refuses a setting change once the router compiled its
// trie, because the serving goroutines read these fields without a lock.
func (r *Router[C]) mustNotBeServing(what string) {
	if r.root.started.Load() {
		panic("router: cannot change " + what + " after the router started serving")
	}
}

// NotFound sets the handler for a request that matches no route. The
// middleware of the root applies to it.
//
// It takes precedence over the error handler: once set, a request that matches
// no route goes to this handler and never reaches [Router.ErrorHandler].
// Without it the router returns [ErrNotFound], which the error handler renders.
func (r *Router[C]) NotFound(h HandlerFunc[C]) {
	r.mustNotBeServing("the not-found handler")
	r.notFound = h
}

// MethodNotAllowed sets the handler for a request whose path matches a route
// but whose method does not. The router sets the Allow header before it calls
// the handler.
//
// It takes precedence over the error handler in the same way [Router.NotFound]
// does. Without it the router returns [ErrMethodNotAllowed].
func (r *Router[C]) MethodNotAllowed(h HandlerFunc[C]) {
	r.mustNotBeServing("the method-not-allowed handler")
	r.methodNotAllowed = h
}

// ErrorHandler sets the one function that renders every failure of a request:
//
//   - an error that a handler or a middleware returned,
//   - a panic that escaped the handler chain, as a 500 whose internal cause
//     carries the panic and the stack,
//   - a request that matched no route, as [ErrNotFound],
//   - a request whose method no route answers, as [ErrMethodNotAllowed].
//
// The last two reach it only while [Router.NotFound] and
// [Router.MethodNotAllowed] are unset. A handler set there answers the request
// itself and wins.
//
// The default is [DefaultErrorHandler]. A panic inside the error handler is
// logged and answered with a bare 500, so a broken renderer cannot loop.
func (r *Router[C]) ErrorHandler(h ErrorHandlerFunc[C]) {
	r.mustNotBeServing("the error handler")
	r.errHandler = h
}

// HandleOPTIONS controls the automatic answer to an OPTIONS request that no
// route handles: status 204 with an Allow header. It is on by default.
func (r *Router[C]) HandleOPTIONS(on bool) {
	r.mustNotBeServing("the OPTIONS setting")
	r.root.autoOptions = on
}

// MaxBodyBytes caps the request body that [Base.Bind] and its variants read.
// The default is [DefaultMaxBodyBytes]. Zero removes the cap.
func (r *Router[C]) MaxBodyBytes(n int64) {
	r.mustNotBeServing("the body limit")
	r.root.maxBody = n
}

// JSONOptions sets the encoding/json/v2 options that [Base.JSON] and
// [Base.BindJSON] apply by default. A per-call option overrides them.
//
//	r.JSONOptions(json.RejectUnknownMembers(true))
func (r *Router[C]) JSONOptions(opts ...json.Options) {
	r.mustNotBeServing("the JSON options")
	r.root.jsonOpts = opts
}

// RedirectTrailingSlash makes the router answer 301 for a path that ends in
// "/" when the path without it has a route. By default the router treats
// "/users/" and "/users" as the same path and answers directly.
func (r *Router[C]) RedirectTrailingSlash(on bool) {
	r.mustNotBeServing("the trailing slash setting")
	r.root.redirectSlash = on
}

// Routes returns every registered route, sorted by host and then by pattern.
// It compiles the trie if the router has not served a request yet.
func (r *Router[C]) Routes() []Route {
	root := r.root
	root.once.Do(root.build)
	return slices.Clone(root.routes)
}

// ---------------------------------------------------------------------------
// Build
// ---------------------------------------------------------------------------

// build compiles the scopes into one trie. It panics on a malformed or
// conflicting pattern, because a route that never matches is a programming
// error and not a request error.
func (r *Router[C]) build() {
	// Publish the trie and the fallbacks first. A malformed pattern panics
	// below, and a caller that recovers must not then meet a nil trie.
	r.tree = new(node[C])
	r.notFoundChain = chain(r.notFound, r.mws)
	r.notAllowedChain = chain(r.methodNotAllowed, r.mws)
	r.optionsChain = chain(autoOptions[C], r.mws)
	r.started.Store(true)

	open := make(map[*Router[C]]bool)

	var walk func(rt *Router[C], prefix string, mws []Middleware[C], host *hostEntry[C])
	walk = func(rt *Router[C], prefix string, mws []Middleware[C], host *hostEntry[C]) {
		if open[rt] {
			panic("router: a router is mounted inside itself")
		}
		open[rt] = true
		defer delete(open, rt)

		p := joinPattern(prefix, rt.prefix)
		m := concatMiddleware(mws, rt.mws)

		// The handler chain of a route does not depend on the host, so build
		// it once even when the scope answers several of them.
		handlers := make([]HandlerFunc[C], len(rt.regs))
		for i, reg := range rt.regs {
			handlers[i] = chain(reg.handler, concatMiddleware(m, reg.mws))
		}

		// A scope that carries host patterns runs once per pattern, so the
		// same routes reach every host that the scope claims. A scope that
		// carries none runs once, against the host it inherited.
		rounds := max(len(rt.hosts), 1)
		for i := range rounds {
			e := host
			if len(rt.hosts) > 0 {
				if host != nil {
					panic("router: a host scope cannot sit inside another host scope")
				}
				e = r.hostEntry(rt.hosts[i])
				// The fallbacks of the host wrap in the middleware of the
				// scope that opened it, so a CORS preflight or a log line
				// still sees a 404 that this host answered. Building them is
				// left until the walk ends, because a scope below may still
				// set the fallbacks of the root that they fall back to.
				if !e.haveMWs {
					e.mws, e.haveMWs = m, true
				}
			}

			tree, names := r.tree, []string(nil)
			if e != nil {
				tree, names = e.tree, e.names
			} else if len(rt.regs) > 0 {
				r.anyHostRoutes = true
			}

			// A fallback that the scope sets belongs to the host it sits in,
			// or to the root when it sits outside every host scope.
			notFound, notAllowed, errh := r.fallbacks(e)
			if rt.notFound != nil {
				*notFound = chain(rt.notFound, m)
				if e == nil {
					r.notFound = rt.notFound // what a host inherits
				}
			}
			if rt.methodNotAllowed != nil {
				*notAllowed = chain(rt.methodNotAllowed, m)
				if e == nil {
					r.methodNotAllowed = rt.methodNotAllowed
				}
			}
			if rt.errHandler != nil {
				*errh = rt.errHandler
			}

			for i, reg := range rt.regs {
				if err := tree.insert(reg.method, joinPattern(p, reg.pattern), names, handlers[i]); err != nil {
					panic(err.Error())
				}
			}
			for _, ch := range rt.children {
				walk(ch, p, m, e)
			}
		}
	}
	walk(r, "", nil, nil)

	r.collectRoutes()
	if r.hostSet != nil {
		// r.notFound and r.methodNotAllowed are final now, so a host that set
		// no fallback of its own inherits the one the application chose,
		// wherever the scope that chose it sat.
		for _, e := range r.hostSet.all {
			if e.optionsChain == nil {
				e.optionsChain = chain(autoOptions[C], e.mws)
			}
			if e.notFoundChain == nil {
				e.notFoundChain = chain(r.notFound, e.mws)
			}
			if e.notAllowedChain == nil {
				e.notAllowedChain = chain(r.methodNotAllowed, e.mws)
			}
		}
		slices.SortStableFunc(r.hostSet.pats, func(a, b *hostEntry[C]) int {
			return lessSpecific(&a.hostSpec, &b.hostSpec)
		})
	}
}

// autoOptions answers an OPTIONS request that no route handles. The Allow
// header is already set when it runs.
func autoOptions[C Context](c C) error { return c.base().NoContent(http.StatusNoContent) }

// fallbacks returns the slots that a scope writes its fallbacks into: the ones
// of the host that the scope sits in, or the ones of the root.
func (r *Router[C]) fallbacks(e *hostEntry[C]) (notFound, notAllowed *HandlerFunc[C], errh *ErrorHandlerFunc[C]) {
	if e != nil {
		return &e.notFoundChain, &e.notAllowedChain, &e.errHandler
	}
	return &r.notFoundChain, &r.notAllowedChain, &r.errHandler
}

// hostEntry returns the entry of a host pattern and creates it on first use.
// Two scopes that name the same host share one entry, and so one route tree.
func (r *Router[C]) hostEntry(spec hostSpec) *hostEntry[C] {
	if r.hostSet == nil {
		r.hostSet = new(hostSet[C])
	}
	hs := r.hostSet
	for _, e := range hs.all {
		if e.pattern == spec.pattern {
			return e
		}
	}

	e := &hostEntry[C]{hostSpec: spec, tree: new(node[C]), idx: int32(len(hs.all))}
	hs.all = append(hs.all, e)
	switch {
	case spec.any:
		hs.any = e
	case spec.exact():
		if hs.exact == nil {
			hs.exact = make(map[string]*hostEntry[C], 4)
		}
		hs.exact[spec.pattern] = e
	default:
		hs.pats = append(hs.pats, e)
	}
	return e
}

// collectRoutes fills the route table that [Router.Routes] returns.
func (r *Router[C]) collectRoutes() {
	add := func(host string) func(pattern, method string) {
		return func(pattern, method string) {
			r.routes = append(r.routes, Route{Host: host, Method: method, Pattern: pattern})
		}
	}
	r.tree.walk(add(""))
	if r.hostSet != nil {
		for _, e := range r.hostSet.all {
			e.tree.walk(add(e.pattern))
		}
	}
	// The tree walks in match order, which is an implementation detail. Sort,
	// so that Routes reads the same way whatever shape the tree took.
	slices.SortFunc(r.routes, func(a, b Route) int {
		if c := strings.Compare(a.Host, b.Host); c != 0 {
			return c
		}
		if c := strings.Compare(a.Pattern, b.Pattern); c != 0 {
			return c
		}
		return strings.Compare(a.Method, b.Method)
	})
}

// concatMiddleware joins two chains into a new slice.
func concatMiddleware[C Context](a, b []Middleware[C]) []Middleware[C] {
	switch {
	case len(a) == 0:
		return b
	case len(b) == 0:
		return a
	default:
		out := make([]Middleware[C], 0, len(a)+len(b))
		out = append(out, a...)
		return append(out, b...)
	}
}

// ---------------------------------------------------------------------------
// Serving
// ---------------------------------------------------------------------------

// ServeHTTP implements [http.Handler].
func (r *Router[C]) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	root := r.root
	root.once.Do(root.build)

	c := root.acquire(w, req)
	defer root.release(c)
	root.route(c, req)
}

// acquire returns a context that is bound to the request. It takes one from
// the pool when [NewPooled] created the router.
func (r *Router[C]) acquire(w http.ResponseWriter, req *http.Request) C {
	var c C
	if r.pool != nil {
		c = r.pool.Get().(C)
	} else {
		c = r.newCtx(w, req)
	}
	b := c.base()
	b.init(w, req)
	b.maxBody = r.maxBody
	b.jsonOpts = r.jsonOpts
	return c
}

// release ends the request. It turns a panic that escaped the handler chain
// into an error for the error handler, and returns the context to the pool.
//
// A panic leaves the context in an unknown state, so release drops it instead
// of pooling it.
func (r *Router[C]) release(c C) {
	if rec := recover(); rec != nil {
		// http.ErrAbortHandler is how a handler asks the server to drop the
		// connection without a log entry. Let it through.
		if rec == http.ErrAbortHandler {
			panic(rec)
		}
		r.handleError(c, PanicError(rec))
		return
	}
	if r.pool != nil {
		r.reset(c)
		r.pool.Put(c)
	}
}

// route matches the request and runs the handler that answers it.
func (r *Router[C]) route(c C, req *http.Request) {
	b := c.base()

	path, escaped := requestPath(req.URL)
	b.pathEscaped = escaped
	if path == "" || path[0] != '/' {
		path = "/" + path
	}
	trimmed := path
	for len(trimmed) > 1 && trimmed[len(trimmed)-1] == '/' {
		trimmed = trimmed[:len(trimmed)-1]
	}

	// Resolve the host once, before the path. A router without host scopes
	// skips this whole step.
	var (
		host *hostEntry[C]
		// hostVals holds the values of the host parameters. Every later walk
		// appends behind it rather than over it, so the values that the
		// fallbacks read survive a walk that matches nothing.
		hostVals = b.paramArr[:0]
	)
	if r.hostSet != nil {
		b.host, b.hostKnown = normalizeHost(req.Host), true
		host, hostVals = r.hostSet.match(b.host, hostVals)
		if host != nil {
			b.hostIdx, b.hostPattern = host.idx, host.pattern
			// Publish the host parameters before the path match, so that a
			// fallback and an error handler read them even when no route
			// answers. A match below replaces them with its own set, which
			// starts with the same values.
			b.setRoute("", host.names, hostVals)
		}
	}

	if r.redirectSlash && trimmed != path && r.canMatch(host, trimmed, req.Method, hostVals[len(hostVals):]) {
		redirectTo(b.res, req, trimmed, escaped)
		return
	}

	var (
		hostSt, anySt matchState[C]
		n             *node[C]
		vals          []string
		nHost         int
	)
	if host != nil {
		n, vals = search(host.tree, trimmed, req.Method, hostVals, &hostSt)
		nHost = len(hostVals)
	}
	// A route that no host scope registered answers every host, so it is the
	// fallback when the routes of the matched host do not fit the path.
	if n == nil && r.anyHostRoutes {
		if m, v := search(r.tree, trimmed, req.Method, hostVals[len(hostVals):], &anySt); m != nil {
			n, vals, nHost = m, v, 0
			// The matched route carries no host, so the fallbacks of the root
			// render it.
			b.hostIdx, b.hostPattern = -1, ""
		}
	}

	switch {
	case n != nil:
		if n.kind == edgeWildcard && len(vals) > 0 {
			b.rawTail = vals[len(vals)-1]
		}
		if escaped {
			// A host never carries a percent escape, so only the values that
			// the path contributed need decoding.
			unescapeParams(vals[nHost:])
		}
		b.setRoute(n.pattern, n.names, vals)
		r.dispatch(c, n.handler(req.Method))

	case hostSt.pathMatch != nil || anySt.pathMatch != nil:
		match, matched, skip := hostSt.pathMatch, hostSt.pathVals, len(hostVals)
		if match == nil {
			// The path belongs to a route that answers every host, so the
			// fallbacks of the root render it, as they do for a full match.
			match, matched, skip = anySt.pathMatch, anySt.pathVals, 0
			host = nil
			b.hostIdx, b.hostPattern = -1, ""
		}
		if escaped {
			unescapeParams(matched[skip:])
		}
		b.setRoute(match.pattern, match.names, matched)
		b.res.Header().Set(HeaderAllow, allowHeader(hostSt.pathMatch, anySt.pathMatch))

		if req.Method == http.MethodOptions && r.autoOptions {
			// The middleware of the root runs here too, so that a CORS
			// preflight reaches the CORS middleware.
			options := r.optionsChain
			if host != nil {
				options = host.optionsChain
			}
			r.dispatch(c, options)
			return
		}
		notAllowed := r.notAllowedChain
		if host != nil {
			notAllowed = host.notAllowedChain
		}
		r.dispatch(c, notAllowed)

	default:
		notFound := r.notFoundChain
		if host != nil {
			notFound = host.notFoundChain
		}
		r.dispatch(c, notFound)
	}
}

// allowHeader joins the methods that the matched nodes answer. Two nodes reach
// it when a host tree and the host-free tree both hold the path under
// different methods.
func allowHeader[C Context](a, b *node[C]) string {
	switch {
	case a == nil:
		return strings.Join(b.allowed(), ", ")
	case b == nil:
		return strings.Join(a.allowed(), ", ")
	}
	out := a.allowed()
	for _, m := range b.allowed() {
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	slices.Sort(out)
	return strings.Join(out, ", ")
}

// requestPath returns the path that the trie matches against, and reports
// whether that path is still percent encoded.
//
// net/url fills RawPath only when the raw request path differs from the
// canonical escaping of the decoded path, which happens exactly when a segment
// carries an escaped separator such as %2F. Every other request already has
// the decoded path in Path, so matching reads it directly instead of asking
// EscapedPath to rescan the whole path, and the parameters need no decoding
// afterwards.
//
// That distinction also keeps a literal percent intact: "/a%25b" arrives as
// Path "/a%b" with an empty RawPath, and decoding it a second time would turn
// "%252F" into a separator that the client never sent.
func requestPath(u *url.URL) (path string, escaped bool) {
	if u.RawPath != "" {
		return u.RawPath, true
	}
	return u.Path, false
}

// dispatch runs a handler and hands any error to the error handler.
func (r *Router[C]) dispatch(c C, h HandlerFunc[C]) {
	if err := h(c); err != nil {
		r.handleError(c, err)
	}
}

// handleError runs the error handler. It catches a panic from that handler
// too, because the alternative is an endless loop of failed renderings.
func (r *Router[C]) handleError(c C, err error) {
	defer func() {
		rec := recover()
		if rec == nil {
			return
		}
		if rec == http.ErrAbortHandler {
			panic(rec)
		}
		b := c.base()
		slog.ErrorContext(b.req.Context(), "router: the error handler panicked",
			slog.Any("panic", rec), slog.Any("error", err))
		if !b.res.Committed {
			b.res.WriteHeader(http.StatusInternalServerError)
		}
	}()
	r.errorHandlerFor(c.base())(c, err)
}

// errorHandlerFor returns the error handler of the matched host, or the one of
// the root.
func (r *Router[C]) errorHandlerFor(b *Base) ErrorHandlerFunc[C] {
	if b.hostIdx >= 0 && r.hostSet != nil {
		if h := r.hostSet.all[b.hostIdx].errHandler; h != nil {
			return h
		}
	}
	return r.errHandler
}

// canMatch reports whether the path has a route for the method, on the matched
// host or on no host at all. scratch is the room that the walk fills with
// parameter values, which the answer discards.
func (r *Router[C]) canMatch(host *hostEntry[C], path, method string, scratch []string) bool {
	if host != nil {
		var st matchState[C]
		if n, _ := search(host.tree, path, method, scratch, &st); n != nil || st.pathMatch != nil {
			return true
		}
	}
	if !r.anyHostRoutes {
		return false
	}
	var st matchState[C]
	n, _ := search(r.tree, path, method, scratch, &st)
	return n != nil || st.pathMatch != nil
}

// redirectTo points the client at the same URL with a new path. It answers 308
// for a method other than GET or HEAD, because a 301 makes some clients repeat
// the request as a GET and drop the body.
func redirectTo(w http.ResponseWriter, req *http.Request, path string, escaped bool) {
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
	http.Redirect(w, req, u.RequestURI(), status)
}
