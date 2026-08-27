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

	// The fields below belong to the root only.
	newCtx           func(http.ResponseWriter, *http.Request) C
	pool             *sync.Pool
	reset            func(C)
	once             sync.Once
	started          atomic.Bool
	tree             *node[C]
	routes           []Route
	notFound         HandlerFunc[C]
	methodNotAllowed HandlerFunc[C]
	errHandler       ErrorHandlerFunc[C]
	notFoundChain    HandlerFunc[C]
	notAllowedChain  HandlerFunc[C]
	optionsChain     HandlerFunc[C]
	autoOptions      bool
	redirectSlash    bool
	maxBody          int64
	jsonOpts         []json.Options
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
	c := &Router[C]{root: r.root, prefix: prefix, mws: mws}
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
	r.root.notFound = h
}

// MethodNotAllowed sets the handler for a request whose path matches a route
// but whose method does not. The router sets the Allow header before it calls
// the handler.
//
// It takes precedence over the error handler in the same way [Router.NotFound]
// does. Without it the router returns [ErrMethodNotAllowed].
func (r *Router[C]) MethodNotAllowed(h HandlerFunc[C]) {
	r.mustNotBeServing("the method-not-allowed handler")
	r.root.methodNotAllowed = h
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
	r.root.errHandler = h
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

// Routes returns every registered route, sorted by pattern. It compiles the
// trie if the router has not served a request yet.
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
	tree := new(node[C])

	// Publish the trie and the fallbacks first. A malformed pattern panics
	// below, and a caller that recovers must not then meet a nil trie.
	r.tree = tree
	r.notFoundChain = chain(r.notFound, r.mws)
	r.notAllowedChain = chain(r.methodNotAllowed, r.mws)
	r.optionsChain = chain(func(c C) error { return c.base().NoContent(http.StatusNoContent) }, r.mws)
	r.started.Store(true)

	open := make(map[*Router[C]]bool)

	var walk func(rt *Router[C], prefix string, mws []Middleware[C])
	walk = func(rt *Router[C], prefix string, mws []Middleware[C]) {
		if open[rt] {
			panic("router: a router is mounted inside itself")
		}
		open[rt] = true
		defer delete(open, rt)

		p := joinPattern(prefix, rt.prefix)
		m := concatMiddleware(mws, rt.mws)

		for _, reg := range rt.regs {
			h := chain(reg.handler, concatMiddleware(m, reg.mws))
			if err := tree.insert(reg.method, joinPattern(p, reg.pattern), h); err != nil {
				panic(err.Error())
			}
		}
		for _, ch := range rt.children {
			walk(ch, p, m)
		}
	}
	walk(r, "", nil)

	tree.walk(func(pattern, method string) {
		r.routes = append(r.routes, Route{Method: method, Pattern: pattern})
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

	if r.redirectSlash && trimmed != path && r.canMatch(trimmed, req.Method) {
		redirectTo(b.res, req, trimmed, escaped)
		return
	}

	var st matchState[C]
	n, vals := search(r.tree, trimmed, req.Method, b.paramArr[:0], &st)

	switch {
	case n != nil:
		if n.seg.kind == segWildcard && len(vals) > 0 {
			b.rawTail = vals[len(vals)-1]
		}
		if escaped {
			unescapeParams(vals)
		}
		b.setRoute(n.pattern, n.names, vals)
		r.dispatch(c, n.handler(req.Method))

	case st.pathMatch != nil:
		if escaped {
			unescapeParams(st.pathVals)
		}
		b.setRoute(st.pathMatch.pattern, st.pathMatch.names, st.pathVals)
		b.res.Header().Set(HeaderAllow, strings.Join(st.pathMatch.allowed(), ", "))
		if req.Method == http.MethodOptions && r.autoOptions {
			// The middleware of the root runs here too, so that a CORS
			// preflight reaches the CORS middleware.
			r.dispatch(c, r.optionsChain)
			return
		}
		r.dispatch(c, r.notAllowedChain)

	default:
		r.dispatch(c, r.notFoundChain)
	}
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
	r.errHandler(c, err)
}

// canMatch reports whether the path has a route for the method.
func (r *Router[C]) canMatch(path, method string) bool {
	var st matchState[C]
	n, _ := search(r.tree, path, method, nil, &st)
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
