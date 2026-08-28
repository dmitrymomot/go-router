package router

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Route describes one registered route. [Router.Routes] returns them.
type Route struct {
	Method  string
	Pattern string

	// Host is the host pattern of the scope that registered the route. It is
	// empty for a route that answers every host. It follows the two fields
	// above, which are the ones that a route table is usually keyed by.
	Host string

	// Name is the name that [Router.Name] gave the route, empty for a route
	// that carries none. [Router.URL] builds a path from it.
	Name string

	// Meta is the value that [Router.Meta] attached to the route. The router
	// never reads it; it carries whatever a generator outside this module puts
	// on a route, such as an OpenAPI operation. A value that is not comparable
	// makes the Route that holds it uncomparable too, so compare the fields
	// that a test cares about rather than the whole struct.
	Meta any
}

// MethodQuery is the QUERY method of RFC 10008. It is safe and idempotent like
// GET and carries a body like POST, which is what lets a client send a query
// too large for a URL.
//
// net/http declares no constant for it yet.
const MethodQuery = "QUERY"

// anyMethod is the method of the entry that [Router.Any] registers. A node
// answers a request with it once no explicit method of the node fits, so one
// entry covers every method, including a WebDAV or a QUERY request that no
// list of standard methods holds.
//
// The value is the one that [Route] reports for such a route, and no request
// can spell it: net/http rejects a method that is not a token, and "*" is the
// wildcard of a request line that never reaches a handler.
const anyMethod = "*"

// registration is one route as the application declared it, before the router
// resolves prefixes and middleware.
type registration[C Context] struct {
	method  string
	pattern string
	handler HandlerFunc[C]
	mws     []Middleware[C]

	// name and meta come from the scope that [Router.Name] and [Router.Meta]
	// opened. They live beside the trie, so the matcher never reads them.
	name string
	meta any
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

	// name and meta belong to a scope that [Router.Name] or [Router.Meta]
	// opened, and reach the next route that the scope registers. nameUsed
	// marks the name as spent, which is what refuses a second route on it.
	name     string
	meta     any
	tagged   bool
	nameUsed bool

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

	// ropts is the snapshot that build() takes of the settings below. Every
	// context points at it, so binding a request to all of them is one
	// pointer store.
	ropts *routerOpts

	// The settings that ropts snapshots. The setters write them, and a
	// request reads them through ropts and never here.
	maxBody      int64
	maxMultipart int64
	strictBind   bool
	logger       *slog.Logger
	jsonOpts     []json.Options

	// The fields below arrived after the ones above and sit behind them, so
	// that the fields a request reads keep the offsets, and the cache lines,
	// that they had. Only the three that a request touches come first.

	// compiled reports that buildErr finished. started goes up as buildErr
	// begins, so only this one says that the trie is whole, and it is the
	// check that a request pays instead of entering sync.Once.
	compiled atomic.Bool

	// preChain is the chain of [Router.Pre] around the matcher. It is nil
	// while no Pre middleware exists, which is the one check that a request
	// pays for the stage, and preMws is what it was built from.
	preChain HandlerFunc[C]

	// observer is the function that [Router.Observe] set. It is nil for a
	// router that reports nothing, which is the single check that a request
	// pays for the seam.
	observer func(c Context, status int, size int64, d time.Duration, err error)

	preMws     []Middleware[C]
	compileErr error

	// scopes holds the fallbacks of every path scope that carries a prefix,
	// most specific first. A request that no route answered runs the chain of
	// the innermost scope that owns its path.
	scopes []*scopeFallback[C]

	// errScopes holds the scopes above that set an error handler, in the same
	// order. It is empty for a router that sets none, which is the one check
	// that a failed request pays for the stage.
	errScopes []*scopeFallback[C]

	// named maps a route name to the pattern that [Router.URL] fills in, and
	// info carries the name and the value of [Router.Meta] back to
	// [Router.Routes]. Both stay nil until a route uses them.
	named map[string]namedRoute
	info  map[routeKey]routeInfo

	// owner is the scope that opened this one as an extension of itself, which
	// [Router.Name], [Router.Meta] and [Router.With] do. A route that such a
	// scope registers is a route of the owner as the application reads it, so
	// it marks the owner too and [Router.Use] still refuses to arrive after
	// it. It is nil for the root and for a scope that [Router.Group],
	// [Router.Route] or [Router.Host] opened, which are scopes of their own.
	owner *Router[C]
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
	if r.name != "" {
		if r.nameUsed {
			panic("router: the scope named " + r.name + " already registered a route; open another Name scope for " + method + " " + pattern)
		}
		r.nameUsed = true
	}
	// The scope that [Router.Name], [Router.Meta] or [Router.With] opened
	// registers on behalf of the one above it, so the route counts for both
	// and a later Use on either of them panics.
	for s := r; s != nil; s = s.owner {
		s.hasRoutes = true
	}
	r.regs = append(r.regs, registration[C]{
		method:  method,
		pattern: pattern,
		handler: h,
		mws:     slices.Clone(mws),
		name:    r.name,
		meta:    r.meta,
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

// Any registers a handler for every method that no explicit route of the
// pattern answers. One entry covers them all, so the route also answers a
// method that no list holds, such as QUERY or a WebDAV method:
//
//	r.Any("/webhooks/{id}", forward)
//	r.POST("/webhooks/{id}", record)   // POST reaches record, everything else forward
//
// An explicit method always wins, whichever of the two registrations came
// first. Because such a route answers every method, it never produces a 405
// and never contributes to an Allow header; [Router.Routes] reports it once,
// under the method "*".
func (r *Router[C]) Any(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(anyMethod, pattern, h, mws...)
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
// Use panics after the scope registers its first route, because the router
// resolves the middleware of a scope when it compiles, so middleware that
// arrives later would silently wrap the routes above it as well. A route that
// [Router.Name], [Router.Meta] or [Router.With] registered counts as a route of
// the scope that opened them, so r.Name("u").GET(...) closes r to Use the way
// r.GET(...) does. Open a [Router.Group] for middleware that comes later.
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
	c := r.newChild("", slices.Clone(mws))
	c.owner = r
	return c
}

// Pre adds middleware that runs before the router matches. It may replace the
// request with [Base.SetRequest], which is how a path rewrite changes what the
// trie sees:
//
//	r.Pre(func(next router.HandlerFunc[Ctx]) router.HandlerFunc[Ctx] {
//		return func(c *app.Context) error {
//			req := c.Request()
//			if rest, ok := strings.CutPrefix(req.URL.Path, "/old/"); ok {
//				rewritten := req.Clone(req.Context())
//				rewritten.URL.Path = "/new/" + rest
//				c.SetRequest(rewritten)
//			}
//			return next(c)
//		}
//	})
//
// The stage runs on the context, before the host and the path match, so
// [Base.RoutePattern] and [Base.Param] are still empty inside it, and an error
// that a Pre middleware returns reaches the error handler of the root, because
// no host has been resolved yet.
//
// The matcher normalizes the path it receives, so a rewrite decides what
// [Router.RedirectTrailingSlash] sees: the trailing slash of the rewritten
// path is the one that answers 301, and the redirect points at the rewritten
// URL rather than at the one the client sent. A rewrite that means to keep the
// original URL visible has to redirect itself instead.
//
// Only the root accepts Pre, because a scope cannot own a stage that runs
// before matching picks the scope. It panics on a scope, the way [Router.Use]
// panics once a scope has routes.
func (r *Router[C]) Pre(mws ...Middleware[C]) {
	if r.root != r {
		panic("router: Pre belongs to the root router, because it runs before matching picks a scope")
	}
	r.mustNotBeServing("the pre-routing middleware")
	r.preMws = append(r.preMws, mws...)
}

// Name opens a scope whose next route carries the name, which [Router.URL]
// then builds a path from:
//
//	r.Name("user").GET("/users/{id}", getUser)
//	r.URL("user", map[string]string{"id": "7"})   // "/users/7"
//
// A second route on the same scope, and a name that another route already
// carries, are both registration errors.
func (r *Router[C]) Name(name string) *Router[C] {
	if name == "" {
		panic("router: Name needs a name")
	}
	c := r.tag()
	c.name = name
	return c
}

// Meta opens a scope whose next routes carry v, which [Router.Routes] reports
// back. The router never reads it, so it holds whatever a generator outside
// this module puts on a route:
//
//	r.Meta(openapi.Op{Summary: "read a user"}).GET("/users/{id}", getUser)
//
// It composes with [Router.Name], in either order:
//
//	r.Name("user").Meta(op).GET("/users/{id}", getUser)
func (r *Router[C]) Meta(v any) *Router[C] {
	c := r.tag()
	c.meta = v
	return c
}

// tag returns the scope that the next route registers on: r itself when
// [Router.Name] or [Router.Meta] already opened one, so that the two of them
// describe one route, and a new scope otherwise.
func (r *Router[C]) tag() *Router[C] {
	if r.root.started.Load() {
		panic("router: cannot name or tag a route after the router started serving")
	}
	if r.tagged {
		return r
	}
	c := r.newChild("", nil)
	c.tagged, c.owner = true, r
	return c
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
// middleware of the scope applies to it.
//
// It takes precedence over the error handler: once set, a request that matches
// no route goes to this handler and never reaches [Router.ErrorHandler].
// Without it the router returns [ErrNotFound], which the error handler renders.
//
// The handler answers the scope that set it. On the root, and on a scope that
// carries no prefix of its own, that is every path; on a scope under a prefix
// it is the paths of that prefix, and the scopes below inherit it. So an API
// branch answers a miss as JSON while the pages around it keep their own page.
func (r *Router[C]) NotFound(h HandlerFunc[C]) {
	r.mustNotBeServing("the not-found handler")
	r.notFound = h
}

// MethodNotAllowed sets the handler for a request whose path matches a route
// but whose method does not. The router sets the Allow header before it calls
// the handler.
//
// It takes precedence over the error handler in the same way [Router.NotFound]
// does, and it answers the scope that set it in the same way. Without it the
// router returns [ErrMethodNotAllowed].
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
// The handler renders the scope that set it, the way [Router.NotFound] does:
// a handler that a scope under a prefix installs answers the paths of that
// prefix alone, so one that exposes the internal cause of a failure never
// renders the failure of a route outside it.
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

// MaxMultipartMemory caps the memory that a multipart body uses before the
// rest of it spills into a temporary file. The default is 32 MiB, the one that
// [http.Request.ParseMultipartForm] applies.
//
// [Router.MaxBodyBytes] still caps the body as a whole, so this setting only
// decides where the parts live while the handler reads them.
func (r *Router[C]) MaxMultipartMemory(n int64) {
	r.mustNotBeServing("the multipart memory limit")
	r.root.maxMultipart = n
}

// StrictBind makes [Base.Bind] and its variants fill only the fields that a
// form, query, json, param or header tag names. It is off by default, so a
// field that no tag names reads the key that its own name spells and a request
// reaches a field the type never meant to expose.
func (r *Router[C]) StrictBind(on bool) {
	r.mustNotBeServing("the strict binding setting")
	r.root.strictBind = on
}

// Logger sets the logger that the router writes its own records to. The
// default error handler reports an internal cause through it, and a handler
// reads it back with [Base.Logger]. Without one the router writes to
// [slog.Default].
//
//	r.Logger(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
func (r *Router[C]) Logger(l *slog.Logger) {
	r.mustNotBeServing("the logger")
	r.root.logger = l
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

// Observe sets the function that the router calls once per request, after the
// request is answered:
//
//	r.Observe(func(c router.Context, status int, size int64, d time.Duration, err error) {
//		requests.WithLabelValues(c.RoutePattern(), strconv.Itoa(status)).Observe(d.Seconds())
//	})
//
// It runs for every request, including one that matched no route, one whose
// method no route answers, and one whose handler panicked, which is what a
// route-level metric needs and what wrapping each handler cannot give. status
// is the one the client saw, which [ResolveStatus] reads from the response or
// from err, size is the number of body bytes, and err is the error that
// reached the error handler, nil when none did.
//
// The observer runs after the error handler wrote the response, so it must not
// write to it. It reads the context as a [Context], because one observer
// serves any router; read the route with [Base.RoutePattern] and the request
// with [Base.Request].
//
// [http.ErrAbortHandler] passes through untouched and reports nothing, because
// it asks the server to drop the connection without a record of the request.
//
// A router without an observer pays a single nil check per request.
func (r *Router[C]) Observe(fn func(c Context, status int, size int64, d time.Duration, err error)) {
	r.mustNotBeServing("the observer")
	r.root.observer = fn
}

// Routes returns every registered route, sorted by host and then by pattern.
// It compiles the trie if the router has not served a request yet.
func (r *Router[C]) Routes() []Route {
	root := r.root
	root.build()
	return slices.Clone(root.routes)
}

// ---------------------------------------------------------------------------
// Build
// ---------------------------------------------------------------------------

// Build compiles the trie now and reports a malformed or conflicting pattern
// instead of panicking:
//
//	if err := r.Build(); err != nil {
//		return fmt.Errorf("route table: %w", err)
//	}
//
// Reach for it when the route table comes from configuration, or in a test
// that asserts a conflict without recover. ServeHTTP calls it implicitly and
// panics on failure, which is the right default for a table written by hand.
//
// Build compiles once. A later call returns the same result and registers
// nothing new, because the router refuses a route after it compiled.
func (r *Router[C]) Build() error { return r.root.compile() }

// compile builds the trie once and remembers the outcome, so that a failed
// [Router.Build] still reaches the request that follows it.
func (r *Router[C]) compile() error {
	r.once.Do(func() { r.compileErr = r.buildErr() })
	return r.compileErr
}

// build compiles the scopes into one trie. It panics on a malformed or
// conflicting pattern, because a route that never matches is a programming
// error and not a request error.
func (r *Router[C]) build() {
	if err := r.compile(); err != nil {
		panic(err.Error())
	}
}

// buildErr compiles the scopes into one trie and reports a malformed or
// conflicting pattern.
func (r *Router[C]) buildErr() error {
	// Publish the trie, the settings and the fallbacks first. A malformed
	// pattern fails below, and a caller that carries on must not then meet a
	// nil trie.
	r.tree = new(node[C])
	r.ropts = &routerOpts{
		jsonOpts:     r.jsonOpts,
		logger:       r.logger,
		maxBody:      r.maxBody,
		maxMultipart: r.maxMultipart,
		strictBind:   r.strictBind,
	}
	r.notFoundChain = chain(r.notFound, r.mws)
	r.notAllowedChain = chain(r.methodNotAllowed, r.mws)
	r.optionsChain = chain(autoOptions[C], r.mws)
	r.started.Store(true)

	open := make(map[*Router[C]]bool)

	// pending collects the path scopes and the fallbacks that a host scope
	// set. Both are resolved after the walk, because a scope below may still
	// replace the fallback of the root that they fall back to.
	var pending []pendingScope[C]

	var walk func(rt *Router[C], prefix string, mws []Middleware[C], host *hostEntry[C], depth int, inherited scopeFallbacks[C]) error
	walk = func(rt *Router[C], prefix string, mws []Middleware[C], host *hostEntry[C], depth int, inherited scopeFallbacks[C]) error {
		if open[rt] {
			return errors.New("router: a router is mounted inside itself")
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
					return errors.New("router: a host scope cannot sit inside another host scope")
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

			// A fallback belongs to the scope that set it. A scope that covers
			// a whole host, or the whole router, writes into the slots that
			// answer for it. A scope that sits under a prefix keeps its own,
			// so that the 404 of an API branch never answers a page outside
			// it and a handler that exposes an internal cause never renders
			// the failure of an unrelated route.
			own := inherited
			switch {
			case rt != r && rt.root == rt:
				// A router that [Router.Mount] attached is a root of its own,
				// and the root that serves the request supplies the
				// fallbacks. The defaults that New left on the mounted router
				// are skipped rather than written over the ones that the
				// application chose.

			case len(rt.hosts) > 0 || normalizePattern(p) == "/":
				notFound, notAllowed, errh := r.fallbacks(e)
				if rt.notFound != nil {
					*notFound = chain(rt.notFound, m)
					if e == nil {
						r.notFound = rt.notFound // what a host inherits
					} else {
						e.rawNotFound = rt.notFound
					}
				}
				if rt.methodNotAllowed != nil {
					*notAllowed = chain(rt.methodNotAllowed, m)
					if e == nil {
						r.methodNotAllowed = rt.methodNotAllowed
					} else {
						e.rawNotAllowed = rt.methodNotAllowed
					}
				}
				if rt.errHandler != nil {
					*errh = rt.errHandler
				}
			default:
				sets := own.take(rt)
				// A scope that carries a prefix owns the fallbacks below it,
				// so that a 404 under /api still runs the middleware of /api.
				// A scope without a prefix of its own needs an entry as soon
				// as it sets one, because the fallback then answers for the
				// prefix that the scope sits under.
				if normalizePattern(rt.prefix) != "/" || sets {
					pending = append(pending, pendingScope[C]{prefix: p, host: e, mws: m, depth: depth, fb: own})
				}
			}

			for i, reg := range rt.regs {
				full := joinPattern(p, reg.pattern)
				if err := tree.insert(reg.method, full, names, handlers[i]); err != nil {
					return err
				}
				if err := r.describe(reg, e, normalizePattern(full)); err != nil {
					return err
				}
			}
			for _, ch := range rt.children {
				if err := walk(ch, p, m, e, depth+1, own); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(r, "", nil, nil, 0, scopeFallbacks[C]{}); err != nil {
		return err
	}

	r.collectRoutes()
	if len(r.preMws) > 0 {
		r.preChain = chain(r.preTerminal, r.preMws)
	}
	// r.notFound and r.methodNotAllowed are final now, so a host and a path
	// scope that set no fallback of their own inherit the one the application
	// chose, wherever the scope that chose it sat.
	if r.hostSet != nil {
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
	for _, ps := range pending {
		s := &scopeFallback[C]{prefix: ps.prefix, hostIdx: -1, depth: ps.depth}
		if ps.host != nil {
			s.hostIdx = ps.host.idx
		}
		s.segs, s.statics = countSegments(ps.prefix)
		// The scope answers with its own handler, with the one of the scope
		// that encloses it, or with the one of its host or of the root.
		notFound, notAllowed := ps.fb.notFound, ps.fb.notAllowed
		if notFound == nil {
			notFound = r.notFound
			if ps.host != nil && ps.host.rawNotFound != nil {
				notFound = ps.host.rawNotFound
			}
		}
		if notAllowed == nil {
			notAllowed = r.methodNotAllowed
			if ps.host != nil && ps.host.rawNotAllowed != nil {
				notAllowed = ps.host.rawNotAllowed
			}
		}
		s.notFoundChain = chain(notFound, ps.mws)
		s.notAllowedChain = chain(notAllowed, ps.mws)
		s.optionsChain = chain(autoOptions[C], ps.mws)
		s.errHandler = ps.fb.errHandler
		r.scopes = append(r.scopes, s)
	}
	// Most specific first, so that the first prefix that covers a path is the
	// innermost scope that registered it. Two scopes that cover the same
	// prefix rank the deeper one first, which is what puts a group inside
	// /api ahead of the scope that opened /api. The sort is stable, so two
	// scopes that share both keep registration order and the outer one wins.
	slices.SortStableFunc(r.scopes, func(a, b *scopeFallback[C]) int {
		if a.segs != b.segs {
			return b.segs - a.segs
		}
		if a.statics != b.statics {
			return b.statics - a.statics
		}
		return b.depth - a.depth
	})
	for _, s := range r.scopes {
		if s.errHandler != nil {
			r.errScopes = append(r.errScopes, s)
		}
	}
	r.compiled.Store(true)
	return nil
}

// describe records the name and the value that a scope attached to a route.
// Both live beside the trie, so the matcher never reads them.
func (r *Router[C]) describe(reg registration[C], e *hostEntry[C], pattern string) error {
	if reg.name == "" && reg.meta == nil {
		return nil
	}
	host := ""
	if e != nil {
		host = e.pattern
	}
	if reg.name != "" {
		// A scope that names several hosts registers one route per host, so
		// the same name reaching the same pattern is that scope and not a
		// clash.
		if prev, ok := r.named[reg.name]; ok && prev.pattern != pattern {
			return fmt.Errorf("router: the route name %q names both %q and %q", reg.name, prev.pattern, pattern)
		}
		if r.named == nil {
			r.named = make(map[string]namedRoute)
		}
		nr := namedRoute{pattern: pattern, parts: parseURLTemplate(pattern)}
		// The pattern reached the trie first, so it parses again without an
		// error. The segments are what a built path is read back against.
		if segs, _, err := parsePattern(pattern); err == nil {
			nr.segs, nr.recheck = segs, needsRoundTrip(segs)
		}
		r.named[reg.name] = nr
	}
	if r.info == nil {
		r.info = make(map[routeKey]routeInfo)
	}
	r.info[routeKey{host: host, method: reg.method, pattern: pattern}] = routeInfo{name: reg.name, meta: reg.meta}
	return nil
}

// preTerminal ends the chain of [Router.Pre]. It matches the request as the
// chain left it, which is what lets a Pre middleware rewrite the path.
func (r *Router[C]) preTerminal(c C) error { return r.route(c, c.base().req) }

// pendingScope is a path scope that the walk found, before the fallbacks that
// it wraps are final.
type pendingScope[C Context] struct {
	prefix string
	host   *hostEntry[C]
	mws    []Middleware[C]

	// fb holds the fallbacks that the scope owns, and depth is how deeply the
	// scope is nested, which ranks two scopes that cover the same prefix.
	fb    scopeFallbacks[C]
	depth int
}

// scopeFallbacks holds the fallbacks that one path scope owns, as the
// application wrote them. A scope inside another one inherits the ones that it
// leaves unset, so a miss under /api/v1 reaches the handler that /api
// installed. Each one is nil until a path scope sets it, and the host or the
// root answers for it while it is.
type scopeFallbacks[C Context] struct {
	notFound   HandlerFunc[C]
	notAllowed HandlerFunc[C]
	errHandler ErrorHandlerFunc[C]
}

// take copies the fallbacks that the scope sets over the inherited ones, and
// reports whether the scope set any.
func (f *scopeFallbacks[C]) take(rt *Router[C]) bool {
	set := false
	if rt.notFound != nil {
		f.notFound, set = rt.notFound, true
	}
	if rt.methodNotAllowed != nil {
		f.notAllowed, set = rt.methodNotAllowed, true
	}
	if rt.errHandler != nil {
		f.errHandler, set = rt.errHandler, true
	}
	return set
}

// scopeFallback answers a request that no route of a path scope took. Each
// scope that carries a prefix gets one, so that the middleware of the scope
// runs around its own 404 and 405 the way the middleware of a host does.
type scopeFallback[C Context] struct {
	// prefix is the joined prefix of the scope, normalized and never "/".
	prefix string

	// segs is the number of segments of the prefix and statics is how many of
	// them carry no parameter. The two rank one scope against another, and
	// depth breaks a tie between two scopes that cover the same prefix.
	segs    int
	statics int
	depth   int

	// hostIdx is the host that the scope sits in, or -1 for a scope outside
	// every host scope.
	hostIdx int32

	notFoundChain   HandlerFunc[C]
	notAllowedChain HandlerFunc[C]
	optionsChain    HandlerFunc[C]

	// errHandler is the error handler that the scope set, nil for a scope that
	// set none. It carries no middleware, the way the one of a host does.
	errHandler ErrorHandlerFunc[C]
}

// covers reports whether path lies inside the prefix of the scope. A segment
// of the prefix that holds a parameter takes any one segment of the path, so
// the scope of /t/{tenant} still owns the 404 of /t/acme/typo.
func (s *scopeFallback[C]) covers(path string) bool {
	prefix := s.prefix
	for prefix != "" {
		if path == "" {
			return false
		}
		want, prest := cutSegment(prefix)
		got, rest := cutSegment(path)
		if got == "" || !segmentCovers(want, got) {
			return false
		}
		prefix, path = prest, rest
	}
	return true
}

// segmentCovers matches one segment of a scope prefix against one segment of a
// path. A segment that holds a parameter or a catch-all takes any text,
// because the value is the caller's and not the scope's.
func segmentCovers(want, got string) bool {
	if want == "*" || strings.IndexByte(want, '{') >= 0 {
		return true
	}
	return want == got
}

// cutSegment splits "/a/b" into "a" and "/b". Both patterns and paths reach it
// normalized, so each one starts with a separator and ends without one.
func cutSegment(p string) (seg, rest string) {
	p = p[1:] // the leading separator
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], p[i:]
	}
	return p, ""
}

// countSegments returns the number of segments of a normalized pattern and how
// many of them are literal text.
func countSegments(p string) (segs, statics int) {
	for p != "" {
		seg, rest := cutSegment(p)
		segs++
		if seg != "*" && strings.IndexByte(seg, '{') < 0 {
			statics++
		}
		p = rest
	}
	return segs, statics
}

// scopeFor returns the innermost scope of the list that owns the path, or nil
// when no scope claims it. The router keeps one list for the fallbacks and one
// for the error handlers, and both are searched by the same rule: the scope
// sits in the matched host, and its prefix covers the path.
func scopeFor[C Context](scopes []*scopeFallback[C], host *hostEntry[C], path string) *scopeFallback[C] {
	if len(scopes) == 0 {
		return nil
	}
	idx := int32(-1)
	if host != nil {
		idx = host.idx
	}
	for _, s := range scopes {
		if s.hostIdx == idx && s.covers(path) {
			return s
		}
	}
	return nil
}

// fallbackChains returns the chains that answer a request that no route took:
// the ones of the innermost path scope that owns the path, else the ones of
// the matched host, else the ones of the root.
func (r *Router[C]) fallbackChains(host *hostEntry[C], path string) (notFound, notAllowed, options HandlerFunc[C]) {
	if s := scopeFor(r.scopes, host, path); s != nil {
		return s.notFoundChain, s.notAllowedChain, s.optionsChain
	}
	if host != nil {
		return host.notFoundChain, host.notAllowedChain, host.optionsChain
	}
	return r.notFoundChain, r.notAllowedChain, r.optionsChain
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

// routeKey identifies one registered route, which is what carries the name and
// the value of [Router.Meta] back to [Router.Routes].
type routeKey struct{ host, method, pattern string }

// routeInfo is the free-form part of a route. It sits beside the trie, so a
// request never pays for it.
type routeInfo struct {
	name string
	meta any
}

// collectRoutes fills the route table that [Router.Routes] returns.
func (r *Router[C]) collectRoutes() {
	add := func(host string) func(pattern, method string) {
		return func(pattern, method string) {
			rt := Route{Host: host, Method: method, Pattern: pattern}
			if info, ok := r.info[routeKey{host: host, method: method, pattern: pattern}]; ok {
				rt.Name, rt.Meta = info.name, info.meta
			}
			r.routes = append(r.routes, rt)
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

// ServeHTTP implements [http.Handler]. It compiles the trie on the first
// request and panics on a malformed or conflicting pattern; [Router.Build]
// reports the same failure as an error instead.
func (r *Router[C]) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	root := r.root
	if !root.compiled.Load() {
		root.build()
	}
	if root.observer != nil {
		root.serveObserved(w, req)
		return
	}
	c := root.acquire(w, req)
	defer root.release(c)
	if root.preChain != nil {
		//nolint:errcheck // The error handler already ran inside dispatch; the
		// return value is there for serveObserved, which reports it.
		root.dispatch(c, root.preChain)
		return
	}
	//nolint:errcheck // Same as above.
	root.route(c, req)
}

// serveObserved serves one request and reports it to the observer. It repeats
// the shape of ServeHTTP rather than adding to it, because the observer needs
// the clock, the error and the response that the plain path never reads, and
// the plain path is measured.
func (r *Router[C]) serveObserved(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	c := r.acquire(w, req)
	var err error
	defer func() {
		rec := recover()
		if rec == http.ErrAbortHandler {
			panic(rec)
		}
		if rec != nil {
			err = PanicError(rec)
			r.handleError(c, err)
		}
		b := c.base()
		r.observer(c, ResolveStatus(b.res, err), b.res.Size, time.Since(start), err)
		// A panic leaves the context in an unknown state, so it is dropped
		// rather than pooled, and only after the observer has read it.
		if rec == nil {
			r.recycle(c)
		}
	}()
	if r.preChain != nil {
		err = r.dispatch(c, r.preChain)
		return
	}
	err = r.route(c, req)
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
	// One store binds the context to every setting of the router, so a
	// context that comes back out of the pool carries the settings of the
	// router that serves it.
	b.ropts = r.ropts
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
	// This repeats the body of recycle. The two calls in it hold recycle over
	// the inline budget of the compiler, and every request that answers
	// without a panic reaches this line, so a call here costs a frame each
	// time.
	if r.pool != nil {
		r.reset(c)
		r.pool.Put(c)
	}
}

// recycle returns a context to the pool. It does nothing for a router that
// [New] built, which allocates one context per request.
//
// [Router.release] carries the same three lines, because it runs on the path
// that the benchmarks measure and a call would cost it a frame.
func (r *Router[C]) recycle(c C) {
	if r.pool != nil {
		r.reset(c)
		r.pool.Put(c)
	}
}

// route matches the request and runs the handler that answers it. It returns
// the error that reached the error handler, so that an observer reports it.
func (r *Router[C]) route(c C, req *http.Request) error {
	b := c.base()

	path, escaped := requestPath(req.URL)
	b.pathEscaped = escaped
	if path == "" || path[0] != '/' {
		path = "/" + path
	}
	// The scope prefixes went through normalizePattern, so the path that they
	// are compared against is trimmed by the same rule. The loop is written
	// out rather than calling it: route is too large for the inliner to fold
	// the call in, and paying a call on every request costs more than the
	// three lines save. scopeErrorHandler, on the error path, does call it.
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
		return nil
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
		// Publish the route on the request as well, so that standard
		// middleware reached through [WrapMiddleware] labels its span with the
		// pattern instead of the raw URL, whose cardinality is unbounded.
		req.Pattern = n.pattern
		return r.dispatch(c, n.handler(req.Method))

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
		req.Pattern = match.pattern
		b.res.Header().Set(HeaderAllow, allowHeader(&hostSt, &anySt))

		_, notAllowed, options := r.fallbackChains(host, trimmed)
		if req.Method == http.MethodOptions && r.autoOptions {
			// The middleware of the scope runs here too, so that a CORS
			// preflight reaches the CORS middleware.
			return r.dispatch(c, options)
		}
		return r.dispatch(c, notAllowed)

	default:
		notFound, _, _ := r.fallbackChains(host, trimmed)
		return r.dispatch(c, notFound)
	}
}

// allowHeader joins the methods that every node whose pattern matched the path
// answers: the ones of the walk over the tree of the matched host, and the ones
// of the walk over the tree that answers every host.
func allowHeader[C Context](host, anyHost *matchState[C]) string {
	out := host.allowedMethods(nil)
	out = anyHost.allowedMethods(out)
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

// dispatch runs a handler and hands any error to the error handler. It returns
// the error as well, which is what [Router.Observe] reports.
func (r *Router[C]) dispatch(c C, h HandlerFunc[C]) error {
	err := h(c)
	if err != nil {
		r.handleError(c, err)
	}
	return err
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
		b.Logger().ErrorContext(b.req.Context(), "router: the error handler panicked",
			slog.Any("panic", rec), slog.Any("error", err))
		if !b.res.Committed {
			b.res.WriteHeader(http.StatusInternalServerError)
		}
	}()
	r.errorHandlerFor(c.base())(c, err)
}

// errorHandlerFor returns the error handler that renders this failure: the one
// of the innermost path scope that owns the path, then the one of the matched
// host, then the one of the root.
//
// It reads the path off the request rather than off the match, because a
// failure of a middleware and a panic both arrive before a route is known.
func (r *Router[C]) errorHandlerFor(b *Base) ErrorHandlerFunc[C] {
	var host *hostEntry[C]
	if b.hostIdx >= 0 && r.hostSet != nil {
		host = r.hostSet.all[b.hostIdx]
	}
	if len(r.errScopes) > 0 {
		if h := r.scopeErrorHandler(host, b.req.URL); h != nil {
			return h
		}
	}
	if host != nil && host.errHandler != nil {
		return host.errHandler
	}
	return r.errHandler
}

// scopeErrorHandler returns the handler of the innermost path scope that owns
// the path and carries one, or nil when no such scope claims it.
func (r *Router[C]) scopeErrorHandler(host *hostEntry[C], u *url.URL) ErrorHandlerFunc[C] {
	path, _ := requestPath(u)
	if s := scopeFor(r.errScopes, host, normalizePattern(path)); s != nil {
		return s.errHandler
	}
	return nil
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
//
// A path that begins with a second separator collapses to one. The client
// sends the request target verbatim, and net/url reads no authority out of it,
// so "//evil.com/" arrives as a path; a Location that kept it is a
// network-path reference, which the browser resolves against the current
// scheme and follows to another origin.
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
	http.Redirect(w, req, u.RequestURI(), status)
}
