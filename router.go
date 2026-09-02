package router

import (
	"encoding/json/v2"
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

type Route struct {
	Method  string
	Pattern string
	Host    string
	Meta    any

	// Params counts the host and path parameters together. A route over
	// InlineParamBudget costs one allocation per request.
	Params int
}

// InlineParamBudget is how many parameters a route carries before its values
// need a second allocation. See Route.Params.
const InlineParamBudget = maxInlineParams

const MethodQuery = "QUERY"

// No request can spell it: net/http rejects a method that is not a token.
const anyMethod = "*"

type registration[C Context] struct {
	method  string
	pattern string
	handler HandlerFunc[C]
	mws     []Middleware[C]
	meta    any
}

type Router[C Context] struct {
	// The request path reads this block on every request, so it stays together
	// and in front: ServeHTTP walks started, observer, pool and preChain before
	// route touches the trie and the fallbacks.
	root     *Router[C]
	started  atomic.Bool
	observer func(c Context, status int, size int64, d time.Duration, err error)
	pool     *sync.Pool
	preChain HandlerFunc[C]
	newCtx   func(http.ResponseWriter, *http.Request) C
	reset    func(C)
	ropts    *routerOpts

	// eng is the compiled route table. Only the root holds one; every scope
	// reaches it through root.
	eng *engine[C]

	// regMu orders registration against the one-time freeze, so the check that
	// refuses a late route cannot be overtaken by the request that makes it
	// late. No request ever takes it.
	regMu sync.Mutex

	// Registration only, from here down.
	prefix           string
	mws              []Middleware[C]
	regs             []registration[C]
	children         []*Router[C]
	owner            *Router[C]
	hasRoutes        bool
	closed           bool
	refreshDepth     int
	meta             any
	tagged           bool
	hosts            []hostSpec
	inHost           bool
	notFound         HandlerFunc[C]
	methodNotAllowed HandlerFunc[C]
	errHandler       ErrorHandlerFunc[C]
	preMws           []Middleware[C]
	info             map[routeKey]routeInfo
}

// engine is the route table as the request path sees it: everything routing
// reads, and nothing registration keeps.
type engine[C Context] struct {
	// owner dispatches errors and owns the context pool.
	owner   *Router[C]
	tree    *node[C]
	hostSet *hostSet[C]

	allowCache       map[*node[C]]string
	scopes           []*scopeFallback[C]
	errScopes        []*scopeFallback[C]
	notFoundChain    HandlerFunc[C]
	notAllowedChain  HandlerFunc[C]
	optionsChain     HandlerFunc[C]
	rootErrorHandler ErrorHandlerFunc[C]
	autoOptions      bool
	redirectSlash    bool
	anyHostRoutes    bool
}

func newEngine[C Context]() *engine[C] {
	return &engine[C]{
		tree:        new(node[C]),
		allowCache:  map[*node[C]]string{},
		autoOptions: true,
		// A router that never calls a setter still has to answer.
		notFoundChain:    defaultNotFound[C],
		notAllowedChain:  defaultMethodNotAllowed[C],
		optionsChain:     autoOptions[C],
		rootErrorHandler: DefaultErrorHandler[C],
	}
}

func New[C Context](newContext func(http.ResponseWriter, *http.Request) C) *Router[C] {
	if newContext == nil {
		panic("router: New needs a context factory")
	}
	// The fallback fields stay nil until a setter fills one, so "did the caller
	// choose this?" has an answer. refresh substitutes the defaults.
	r := &Router[C]{
		newCtx: newContext,
		ropts:  &routerOpts{maxBody: DefaultMaxBodyBytes},
		eng:    newEngine[C](),
	}
	r.root = r
	r.eng.owner = r
	return r
}

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

func (r *Router[C]) Handle(method, pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.handle(method, pattern, h, mws)
}

func (r *Router[C]) handle(method, pattern string, h HandlerFunc[C], mws []Middleware[C]) {
	// Ordered against the freeze: without it the check below is a guess, and a
	// route could go into a trie a request had already started reading.
	r.root.regMu.Lock()
	defer r.root.regMu.Unlock()
	if r.root.started.Load() {
		panic("router: cannot register " + method + " " + pattern + " after the router started serving")
	}
	r.mustBeOpen("register " + method + " " + pattern)
	if method == "" {
		panic("router: Handle needs a method")
	}
	if h == nil {
		panic("router: Handle needs a handler for " + method + " " + pattern)
	}
	validateMiddleware(mws)
	for s := r; s != nil; s = s.owner {
		s.hasRoutes = true
	}
	reg := registration[C]{
		method:  method,
		pattern: pattern,
		handler: h,
		mws:     slices.Clone(mws),
		meta:    r.meta,
	}
	r.regs = append(r.regs, reg)
	r.install(reg)
}

// install puts the route in its trie now, so a malformed pattern or a conflict
// panics at the line that wrote it rather than at the first request.
func (r *Router[C]) install(reg registration[C]) {
	eng := r.top().eng
	full := joinPattern(r.scopePrefix(), reg.pattern)
	handler := chain(reg.handler, concatMiddleware(r.scopeMiddleware(), reg.mws))

	entries := r.hostEntriesIn(eng)
	if len(entries) == 0 {
		eng.anyHostRoutes = true
		if err := eng.tree.insert(reg.method, full, nil, handler, eng.autoOptions, eng.allowCache); err != nil {
			panic(err.Error())
		}
		r.record(reg, nil, full)
		return
	}
	for _, e := range entries {
		if err := e.tree.insert(reg.method, full, e.names, handler, eng.autoOptions, eng.allowCache); err != nil {
			panic(err.Error())
		}
		r.record(reg, e, full)
	}
}

func (r *Router[C]) record(reg registration[C], e *hostEntry[C], full string) {
	r.top().describe(reg, e, normalizePattern(full))
}

// scopePrefix joins the prefixes of every scope between the root and this one.
func (r *Router[C]) scopePrefix() string {
	prefix := ""
	for _, s := range slices.Backward(r.lineage()) {
		prefix = joinPattern(prefix, s.prefix)
	}
	return prefix
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

// hostEntries returns the entries of the nearest host scope, or none when the
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

func (r *Router[C]) GET(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodGet, pattern, h, mws...)
}

func (r *Router[C]) HEAD(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodHead, pattern, h, mws...)
}

func (r *Router[C]) POST(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodPost, pattern, h, mws...)
}

func (r *Router[C]) PUT(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodPut, pattern, h, mws...)
}

func (r *Router[C]) PATCH(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodPatch, pattern, h, mws...)
}

func (r *Router[C]) DELETE(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodDelete, pattern, h, mws...)
}

func (r *Router[C]) OPTIONS(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodOptions, pattern, h, mws...)
}

func (r *Router[C]) Any(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(anyMethod, pattern, h, mws...)
}

func (r *Router[C]) Match(methods []string, pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	if h == nil {
		panic("router: Match needs a handler for " + pattern)
	}
	// Without this the call registers nothing and says nothing, and the route
	// is missing at the first request instead of at the line that wrote it.
	if len(methods) == 0 {
		panic("router: Match needs at least one method for " + pattern)
	}
	validateMiddleware(mws)
	for _, method := range methods {
		if method == "" {
			panic("router: Match needs non-empty methods")
		}
	}
	for _, m := range methods {
		r.handle(m, pattern, h, mws)
	}
}

func (r *Router[C]) Use(mws ...Middleware[C]) {
	validateMiddleware(mws)
	if r.hasRoutes {
		panic("router: Use must come before the routes of a scope; open a Group for later middleware")
	}
	if r.root.started.Load() {
		panic("router: cannot add middleware after the router started serving")
	}
	r.mws = append(r.mws, mws...)
	r.settingChanged()
}

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

func validateMiddleware[C Context](mws []Middleware[C]) {
	for _, mw := range mws {
		if mw == nil {
			panic("router: middleware must not be nil")
		}
	}
}

func (r *Router[C]) newChild(prefix string, mws []Middleware[C]) *Router[C] {
	validateMiddleware(mws)
	if r.root.started.Load() {
		panic("router: cannot create a scope after the router started serving")
	}
	r.mustBeOpen("open a scope")
	if _, _, err := parsePattern(prefix); err != nil {
		panic(err.Error())
	}
	c := &Router[C]{root: r.root, owner: r, prefix: prefix, mws: mws, inHost: r.inHost || len(r.hosts) > 0}
	r.children = append(r.children, c)
	if normalizePattern(prefix) != "/" {
		c.settingChanged()
	}
	return c
}

func (r *Router[C]) Group(fn func(g *Router[C])) *Router[C] {
	var c *Router[C]
	r.inOneScope(func() {
		c = r.newChild("", nil)
		if fn != nil {
			fn(c)
		}
	})
	return c
}

func (r *Router[C]) Route(prefix string, fn func(g *Router[C])) *Router[C] {
	var c *Router[C]
	r.inOneScope(func() {
		c = r.newChild(prefix, nil)
		if fn != nil {
			fn(c)
		}
	})
	return c
}

func (r *Router[C]) With(mws ...Middleware[C]) *Router[C] {
	return r.newChild("", slices.Clone(mws))
}

func (r *Router[C]) Pre(mws ...Middleware[C]) {
	if r.root != r {
		panic("router: Pre belongs to the root router, because it runs before matching picks a scope")
	}
	r.mustNotBeServing("the pre-routing middleware")
	validateMiddleware(mws)
	r.preMws = append(r.preMws, mws...)
	r.settingChanged()
}

func (r *Router[C]) Meta(v any) *Router[C] {
	c := r.tag()
	c.meta = v
	return c
}

func (r *Router[C]) tag() *Router[C] {
	if r.root.started.Load() {
		panic("router: cannot name or tag a route after the router started serving")
	}
	if r.tagged {
		return r
	}
	c := r.newChild("", nil)
	c.tagged = true
	return c
}

func (r *Router[C]) Host(pattern string, fn func(h *Router[C])) *Router[C] {
	return r.Hosts([]string{pattern}, fn)
}

func (r *Router[C]) Hosts(patterns []string, fn func(h *Router[C])) *Router[C] {
	if len(patterns) == 0 {
		panic("router: Hosts needs at least one pattern")
	}
	specs := make([]hostSpec, 0, len(patterns))
	for _, p := range patterns {
		spec, err := parseHostPattern(p)
		if err != nil {
			panic(err.Error())
		}
		// Two spellings of one host resolve to one entry, so the second copy of
		// every route landed in a trie that already held it and the insert
		// blamed the route rather than the duplicate host.
		if i := slices.IndexFunc(specs, func(o hostSpec) bool { return o.pattern == spec.pattern }); i >= 0 {
			panic("router: Hosts got " + patterns[i] + " and " + p + ", which name the same host " + spec.pattern)
		}
		specs = append(specs, spec)
	}
	if r.root.started.Load() {
		panic("router: cannot register a host scope after the router started serving")
	}
	if r.inHost || len(r.hosts) > 0 {
		panic("router: a host scope cannot sit inside another host scope")
	}
	var c *Router[C]
	r.inOneScope(func() {
		c = r.newChild("", nil)
		c.hosts = specs
		for _, spec := range specs {
			if _, err := r.root.eng.hostEntry(spec); err != nil {
				panic(err.Error())
			}
		}
		if fn != nil {
			fn(c)
		}
		c.settingChanged()
	})
	return c
}

func (r *Router[C]) HostRouter[D Context](pattern string, sub *Router[D]) {
	if sub == nil {
		panic("router: HostRouter needs a router")
	}
	r.HostHandler(pattern, sub)
}

func (r *Router[C]) HostHandler(pattern string, h http.Handler) {
	if h == nil {
		panic("router: HostHandler needs a handler")
	}
	r.Host(pattern, func(g *Router[C]) { g.MountHandler("/", h) })
}

func (r *Router[C]) Mount(prefix string, sub *Router[C]) {
	if sub == nil {
		panic("router: Mount needs a router")
	}
	if sub.root != sub {
		panic("router: Mount needs a top-level router; a scope of another router cannot be mounted, " +
			"because closing it would close the router that owns it")
	}
	if slices.Contains(r.lineage(), sub) {
		panic("router: a router is mounted inside itself")
	}
	sub.mustNotCarryRootOnlySettings()

	shim := r.newChild(prefix, nil)
	shim.children = append(shim.children, sub)
	// The subtree registers into this parent from now on, so replay what it
	// already holds and close it: a later route would have nowhere to go.
	sub.owner = shim
	if sub.hasRoutes {
		// install does not mark the owners, so Use above the mount would pass
		// its guard and apply to none of these routes.
		for s := shim; s != nil; s = s.owner {
			s.hasRoutes = true
		}
	}
	sub.installSubtree()
	closeSubtree(sub)
	r.settingChanged()
}

// closeSubtree shuts a mounted subtree to further registration: its routes are
// replayed into the parent, so a later one would land in a trie nobody serves.
// Per-scope on purpose, so a router can still be mounted into two parents.
func closeSubtree[C Context](r *Router[C]) {
	r.closed = true
	for _, ch := range r.children {
		closeSubtree(ch)
	}
}

// mustNotCarryRootOnlySettings refuses a mount that would lose a setting.
// These live on the root the request path reads, one per served router.
func (r *Router[C]) mustNotCarryRootOnlySettings() {
	lost := ""
	switch {
	case len(r.preMws) > 0:
		lost = "Pre middleware"
	case r.observer != nil:
		lost = "an observer"
	case !r.eng.autoOptions:
		lost = "HandleOPTIONS(false)"
	case r.eng.redirectSlash:
		lost = "RedirectTrailingSlash(true)"
	case r.ropts.maxBody != DefaultMaxBodyBytes:
		lost = "MaxBodyBytes"
	case r.ropts.maxMultipart != 0:
		lost = "MaxMultipartMemory"
	case r.ropts.logger != nil:
		lost = "a logger"
	case len(r.ropts.jsonOpts) > 0:
		lost = "JSONOptions"
	default:
		return
	}
	panic("router: the mounted router carries " + lost +
		", which belongs to the router that serves; set it on the parent instead")
}

func (r *Router[C]) installSubtree() {
	for _, reg := range r.regs {
		r.install(reg)
	}
	for _, ch := range r.children {
		ch.installSubtree()
	}
}

func (r *Router[C]) MountRouter[D Context](prefix string, sub *Router[D]) {
	if sub == nil {
		panic("router: MountRouter needs a router")
	}
	r.MountHandler(prefix, sub)
}

func (r *Router[C]) MountHandler(prefix string, h http.Handler) {
	if h == nil {
		panic("router: MountHandler needs a handler")
	}
	prefix = normalizePattern(prefix)
	handler := func(c C) error {
		b := c.base()
		req := stripMountPrefix(b.req, b.rawTail, b.pathEscaped, b.tailSlash)
		b.SetRequest(req)
		h.ServeHTTP(b.res, req)
		return nil
	}
	r.handle(anyMethod, prefix, handler, nil)
	r.handle(anyMethod, joinPattern(prefix, "/{"+mountParam+"...}"), handler, nil)
}

func stripMountPrefix(r *http.Request, tail string, escaped, tailSlash bool) *http.Request {
	r2 := new(http.Request)
	*r2 = *r
	u := new(url.URL)
	*u = *r.URL
	r2.URL = u

	if tail == "" {
		u.Path, u.RawPath = "/", ""
		return r2
	}
	// The router matched without the trailing slash; the handler below the
	// mount is a stranger to that and needs the path the client sent, or it
	// redirects to a directory URL that routes back through here.
	rest := "/" + tail
	if tailSlash {
		rest += "/"
	}
	u.Path, u.RawPath = rest, ""
	if escaped {
		if unescaped, err := url.PathUnescape(rest); err == nil && unescaped != rest {
			u.Path, u.RawPath = unescaped, rest
		}
	}
	return r2
}

func (r *Router[C]) mustNotBeServing(what string) {
	if r.root.started.Load() {
		panic("router: cannot change " + what + " after the router started serving")
	}
	r.mustBeOpen("change " + what)
}

// mustBeOpen rejects work on a subtree that Mount already replayed and closed.
// Anything added now would sit in a router nobody serves, silently.
func (r *Router[C]) mustBeOpen(what string) {
	if r.closed {
		panic("router: cannot " + what + " on a mounted router; do it before Mount")
	}
}

// mustOwnFallbacks rejects a fallback setter on a scope that cannot express
// one. Scopes are keyed by path prefix, so a prefix-less child covers the whole
// tree and would displace the root's. The root, a host scope and any prefixed
// scope each name a region the router can match.
func (r *Router[C]) mustOwnFallbacks(what string) {
	if r == r.root || len(r.hosts) > 0 || normalizePattern(r.scopePrefix()) != "/" {
		return
	}
	panic("router: a scope without a prefix cannot own " + what +
		"; set it on the router itself, or open a Route with a prefix")
}

func (r *Router[C]) NotFound(h HandlerFunc[C]) {
	if h == nil {
		panic("router: NotFound needs a handler")
	}
	r.mustNotBeServing("the not-found handler")
	r.mustOwnFallbacks("the not-found handler")
	r.notFound = h
	r.settingChanged()
}

func (r *Router[C]) MethodNotAllowed(h HandlerFunc[C]) {
	if h == nil {
		panic("router: MethodNotAllowed needs a handler")
	}
	r.mustNotBeServing("the method-not-allowed handler")
	r.mustOwnFallbacks("the method-not-allowed handler")
	r.methodNotAllowed = h
	r.settingChanged()
}

func (r *Router[C]) ErrorHandler(h ErrorHandlerFunc[C]) {
	if h == nil {
		panic("router: ErrorHandler needs a handler")
	}
	r.mustNotBeServing("the error handler")
	r.mustOwnFallbacks("the error handler")
	r.errHandler = h
	r.settingChanged()
}

func (r *Router[C]) HandleOPTIONS(on bool) {
	r.mustNotBeServing("the OPTIONS setting")
	root := r.root
	root.eng.autoOptions = on
	root.eng.tree.recacheAllow(on, root.eng.allowCache)
	if root.eng.hostSet != nil {
		for _, e := range root.eng.hostSet.all {
			e.tree.recacheAllow(on, root.eng.allowCache)
		}
	}
}

func (r *Router[C]) MaxBodyBytes(n int64) {
	r.mustNotBeServing("the body limit")
	r.root.ropts.maxBody = n
}

func (r *Router[C]) MaxMultipartMemory(n int64) {
	r.mustNotBeServing("the multipart memory limit")
	r.root.ropts.maxMultipart = n
}

func (r *Router[C]) Logger(l *slog.Logger) {
	r.mustNotBeServing("the logger")
	r.root.ropts.logger = l
}

func (r *Router[C]) JSONOptions(opts ...json.Options) {
	r.mustNotBeServing("the JSON options")
	r.root.ropts.jsonOpts = slices.Clone(opts)
}

func (r *Router[C]) RedirectTrailingSlash(on bool) {
	r.mustNotBeServing("the trailing slash setting")
	r.root.eng.redirectSlash = on
}

func (r *Router[C]) Observe(fn func(c Context, status int, size int64, d time.Duration, err error)) {
	r.mustNotBeServing("the observer")
	r.root.observer = fn
}

// Routes reports the table as it stands. Registration stays open afterwards.
func (r *Router[C]) Routes() []Route { return r.top().collectRoutes() }

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
			e.errHandler, e.rawNotFound, e.rawNotAllowed = nil, nil, nil
		}
	}
	// A nil field reads as the package default here, because elsewhere nil is
	// what says nobody chose a handler.
	rootNotFound := HandlerFunc[C](defaultNotFound[C])
	if r.notFound != nil {
		rootNotFound = r.notFound
	}
	rootNotAllowed := HandlerFunc[C](defaultMethodNotAllowed[C])
	if r.methodNotAllowed != nil {
		rootNotAllowed = r.methodNotAllowed
	}
	rootErrHandler := ErrorHandlerFunc[C](DefaultErrorHandler[C])
	if r.errHandler != nil {
		rootErrHandler = r.errHandler
	}
	eng.notFoundChain = chain(rootNotFound, r.mws)
	eng.notAllowedChain = chain(rootNotAllowed, r.mws)
	eng.optionsChain = chain(autoOptions[C], r.mws)
	open := make(map[*Router[C]]bool)

	var pending []pendingScope[C]

	var walk func(rt *Router[C], prefix string, mws []Middleware[C], host *hostEntry[C], depth int, inherited scopeFallbacks[C])
	walk = func(rt *Router[C], prefix string, mws []Middleware[C], host *hostEntry[C], depth int, inherited scopeFallbacks[C]) {
		if open[rt] {
			panic("router: a router is mounted inside itself")
		}
		open[rt] = true
		defer delete(open, rt)

		p := joinPattern(prefix, rt.prefix)
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
					if rt.notFound != nil {
						rootNotFound = rt.notFound
						eng.notFoundChain = chain(rt.notFound, m)
					}
					if rt.methodNotAllowed != nil {
						rootNotAllowed = rt.methodNotAllowed
						eng.notAllowedChain = chain(rt.methodNotAllowed, m)
					}
					if rt.errHandler != nil {
						rootErrHandler = rt.errHandler
					}
				} else {
					if rt.notFound != nil {
						e.notFoundChain = chain(rt.notFound, m)
						e.rawNotFound = rt.notFound
					}
					if rt.methodNotAllowed != nil {
						e.notAllowedChain = chain(rt.methodNotAllowed, m)
						e.rawNotAllowed = rt.methodNotAllowed
					}
					if rt.errHandler != nil {
						e.errHandler = rt.errHandler
					}
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
	walk(r, "", nil, nil, 0, scopeFallbacks[C]{})

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
		segs, names, _ := parsePattern(ps.prefix) //nolint:errcheck // newChild rejected a bad prefix already.
		s := &scopeFallback[C]{prefix: ps.prefix, names: names, pattern: segs, hostIdx: -1, depth: ps.depth, errorIdx: -1}
		if ps.host != nil {
			s.hostIdx = ps.host.idx
		}
		notFound, notAllowed := ps.fb.notFound, ps.fb.notAllowed
		if notFound == nil {
			notFound = rootNotFound
			if ps.host != nil && ps.host.rawNotFound != nil {
				notFound = ps.host.rawNotFound
			}
		}
		if notAllowed == nil {
			notAllowed = rootNotAllowed
			if ps.host != nil && ps.host.rawNotAllowed != nil {
				notAllowed = ps.host.rawNotAllowed
			}
		}
		s.notFoundChain = chain(notFound, ps.mws)
		s.notAllowedChain = chain(notAllowed, ps.mws)
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

// describe records the metadata a route was tagged with, for Routes.
func (r *Router[C]) describe(reg registration[C], e *hostEntry[C], pattern string) {
	if reg.meta == nil {
		return
	}
	host := ""
	if e != nil {
		host = e.pattern
	}
	if r.info == nil {
		r.info = make(map[routeKey]routeInfo)
	}
	r.info[routeKey{host: host, method: reg.method, pattern: pattern}] = routeInfo{meta: reg.meta}
}

func (r *Router[C]) preTerminal(c C) error { return r.eng.route(c, c.base().req, false) }

type pendingScope[C Context] struct {
	prefix string
	host   *hostEntry[C]
	mws    []Middleware[C]
	fb     scopeFallbacks[C]
	depth  int
}

type scopeFallbacks[C Context] struct {
	notFound   HandlerFunc[C]
	notAllowed HandlerFunc[C]
	errHandler ErrorHandlerFunc[C]
}

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

type scopeFallback[C Context] struct {
	prefix          string
	names           []string
	pattern         []segment
	depth           int
	hostIdx         int32
	errorIdx        int32
	notFoundChain   HandlerFunc[C]
	notAllowedChain HandlerFunc[C]
	optionsChain    HandlerFunc[C]
	errHandler      ErrorHandlerFunc[C]
}

func (s *scopeFallback[C]) covers(path string, escaped bool) bool {
	_, ok := s.walk(path, escaped, nil)
	return ok
}

// coversInto is covers, keeping the parameter values it decodes on the way.
func (s *scopeFallback[C]) coversInto(path string, escaped bool, vals []string) ([]string, bool) {
	return s.walk(path, escaped, vals)
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
		if !segmentMatches(want, got) {
			return vals, false
		}
		if vals != nil && want.kind != segStatic {
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

func (e *engine[C]) fallbackChains(
	host *hostEntry[C], path string, escaped bool,
) (scope *scopeFallback[C], notFound, notAllowed, options HandlerFunc[C]) {
	if s := scopeFor(e.scopes, host, path, escaped); s != nil {
		return s, s.notFoundChain, s.notAllowedChain, s.optionsChain
	}
	if host != nil {
		return nil, host.notFoundChain, host.notAllowedChain, host.optionsChain
	}
	return nil, e.notFoundChain, e.notAllowedChain, e.optionsChain
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
	vals, ok := s.coversInto(path, escaped, seed)
	if !ok {
		return
	}
	names := s.names
	if len(b.paramNames) > 0 {
		names = append(slices.Clip(b.paramNames), s.names...)
	}
	b.needsCleanup = true
	b.setRoute(s.prefix, names, vals)
}

func autoOptions[C Context](c C) error { return c.base().NoContent(http.StatusNoContent) }

func (e *engine[C]) mustHostEntry(spec hostSpec) *hostEntry[C] {
	he, err := e.hostEntry(spec)
	if err != nil {
		panic(err.Error())
	}
	return he
}

func (eng *engine[C]) hostEntry(spec hostSpec) (*hostEntry[C], error) {
	if eng.hostSet == nil {
		eng.hostSet = new(hostSet[C])
	}
	hs := eng.hostSet
	for _, e := range hs.all {
		if e.pattern == spec.pattern {
			return e, nil
		}
		if sameHostShape(&e.hostSpec, &spec) {
			return nil, fmt.Errorf("router: host pattern %q has the same match shape as %q but uses different parameter names", spec.pattern, e.pattern)
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
	return e, nil
}

type routeKey struct{ host, method, pattern string }

type routeInfo struct{ meta any }

func (r *Router[C]) collectRoutes() []Route {
	var out []Route
	add := func(host string) func(pattern, method string, params int) {
		return func(pattern, method string, params int) {
			rt := Route{Host: host, Method: method, Pattern: pattern, Params: params}
			if info, ok := r.info[routeKey{host: host, method: method, pattern: pattern}]; ok {
				rt.Meta = info.meta
			}
			out = append(out, rt)
		}
	}
	r.eng.tree.walk(add(""))
	if r.eng.hostSet != nil {
		for _, e := range r.eng.hostSet.all {
			e.tree.walk(add(e.pattern))
		}
	}
	slices.SortFunc(out, func(a, b Route) int {
		if c := strings.Compare(a.Host, b.Host); c != 0 {
			return c
		}
		if c := strings.Compare(a.Pattern, b.Pattern); c != 0 {
			return c
		}
		return strings.Compare(a.Method, b.Method)
	})
	return out
}

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

func (r *Router[C]) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	root := r.root
	if !root.started.Load() {
		root.freeze()
	}
	if root.observer != nil {
		root.serveObserved(w, req)
		return
	}
	c := root.acquire(w, req)
	defer root.release(c)
	if root.preChain != nil {
		//nolint:errcheck // The error handler already ran inside dispatch; the
		root.dispatch(c, root.preChain)
		return
	}
	//nolint:errcheck // Same as above.
	root.eng.route(c, req, true)
}

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
		if rec == nil {
			r.recycle(c)
		}
	}()
	if r.preChain != nil {
		err = r.dispatch(c, r.preChain)
		return
	}
	err = r.eng.route(c, req, true)
}

func (r *Router[C]) acquire(w http.ResponseWriter, req *http.Request) C {
	var c C
	if r.pool != nil {
		c = r.pool.Get().(C)
	} else {
		c = r.newCtx(w, req)
	}
	b := c.base()
	b.init(w, req)
	b.ropts = r.ropts
	return c
}

// The pool lines repeat recycle: a call would cost a frame on every request
// that answers without a panic. A panicked context is dropped, not pooled.
func (r *Router[C]) release(c C) {
	if rec := recover(); rec != nil {
		if rec == http.ErrAbortHandler {
			panic(rec)
		}
		r.handleError(c, PanicError(rec))
		return
	}
	if r.pool != nil {
		b := c.base()
		r.reset(c)
		b.req, b.res = releasedRequest, nil
		b.resStorage.ResponseWriter = nil
		if b.needsCleanup || b.resStorage.before != nil || cap(b.paramVals) > len(b.paramArr) {
			b.clearRequestSlow()
		} else {
			b.paramArr = [maxInlineParams]string{}
		}
		r.pool.Put(c)
	}
}

func (r *Router[C]) recycle(c C) {
	if r.pool != nil {
		b := c.base()
		r.reset(c)
		b.req, b.res = releasedRequest, nil
		b.resStorage.ResponseWriter = nil
		if b.needsCleanup || b.resStorage.before != nil || cap(b.paramVals) > len(b.paramArr) {
			b.clearRequestSlow()
		} else {
			b.paramArr = [maxInlineParams]string{}
		}
		r.pool.Put(c)
	}
}

func (e *engine[C]) route(c C, req *http.Request, handleErrors bool) error {
	b := c.base()

	path, escaped := requestPath(req.URL)
	b.pathEscaped = escaped
	if path == "" || path[0] != '/' {
		path = "/" + path
	}
	// Trimmed by the same rule as the scope prefixes. Written out because route
	// is too large for the inliner to fold a call in.
	trimmed := path
	for len(trimmed) > 1 && trimmed[len(trimmed)-1] == '/' {
		trimmed = trimmed[:len(trimmed)-1]
	}
	b.tailSlash = len(trimmed) != len(path)

	var (
		host     *hostEntry[C]
		hostVals = b.paramArr[:0]
	)
	if e.hostSet != nil {
		var hostOK bool
		b.host, hostOK = normalizeHostOK(req.Host)
		b.hostKnown = true
		if e.owner.pool != nil && b.host != "" {
			b.needsCleanup = true
		}
		if hostOK {
			host, hostVals = e.hostSet.match(b.host, hostVals)
		}
		if host != nil {
			b.hostIdx, b.hostPattern = host.idx, host.pattern
			b.setRoute("", host.names, hostVals)
		}
	}

	if e.redirectSlash && trimmed != path && e.canMatch(host, trimmed, req.Method, hostVals[len(hostVals):], escaped) {
		redirectTo(b.res, req, trimmed, escaped)
		return nil
	}

	var (
		hostSt, anySt matchState[C]
		n             *node[C]
		vals          []string
	)
	if host != nil {
		n, vals = search(host.tree, trimmed, req.Method, hostVals, &hostSt, escaped)
	}
	if n == nil && e.anyHostRoutes {
		if m, v := search(e.tree, trimmed, req.Method, hostVals[len(hostVals):], &anySt, escaped); m != nil {
			n, vals = m, v
			b.hostIdx, b.hostPattern = -1, ""
		}
	}

	switch {
	case n != nil:
		selectedHost := host
		if b.hostIdx < 0 {
			selectedHost = nil
		}
		if len(e.errScopes) > 0 || selectedHost != nil && selectedHost.errHandler != nil {
			e.selectErrorTarget(b, selectedHost, trimmed, escaped)
		}
		if n.kind == edgeWildcard && len(vals) > 0 {
			b.rawTail = vals[len(vals)-1]
			b.needsCleanup = b.needsCleanup || b.rawTail != ""
			if decoded, ok := decodePathSegment(b.rawTail, escaped); ok {
				vals[len(vals)-1] = decoded
			}
		}
		b.setRoute(n.pattern, n.names, vals)
		req.Pattern = n.pattern
		h := n.handler(req.Method)
		if handleErrors {
			return e.owner.dispatch(c, h)
		}
		return h(c)

	case hostSt.pathMatch != nil || anySt.pathMatch != nil:
		match, matched, skip := hostSt.pathMatch, hostSt.pathVals, len(hostVals)
		if match == nil {
			match, matched, skip = anySt.pathMatch, anySt.pathVals, 0
			host = nil
			b.hostIdx, b.hostPattern = -1, ""
		}
		if match.kind == edgeWildcard && len(matched) > skip {
			matched = slices.Clone(matched)
			if decoded, ok := decodePathSegment(matched[len(matched)-1], escaped); ok {
				matched[len(matched)-1] = decoded
			}
		}
		if len(e.errScopes) > 0 || host != nil && host.errHandler != nil {
			e.selectErrorTarget(b, host, trimmed, escaped)
		}
		b.needsCleanup = true
		b.setRoute(match.pattern, match.names, matched)
		req.Pattern = match.pattern
		b.res.Header().Set(HeaderAllow, e.allowHeader(&hostSt, &anySt))

		_, _, notAllowed, options := e.fallbackChains(host, trimmed, escaped)
		if req.Method == http.MethodOptions && e.autoOptions {
			if handleErrors {
				return e.owner.dispatch(c, options)
			}
			return options(c)
		}
		if handleErrors {
			return e.owner.dispatch(c, notAllowed)
		}
		return notAllowed(c)

	default:
		if len(e.errScopes) > 0 || host != nil && host.errHandler != nil {
			e.selectErrorTarget(b, host, trimmed, escaped)
		}
		scope, notFound, _, _ := e.fallbackChains(host, trimmed, escaped)
		if scope != nil {
			scope.bindPrefixParams(b, trimmed, escaped)
		}
		if handleErrors {
			return e.owner.dispatch(c, notFound)
		}
		return notFound(c)
	}
}

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

// net/url fills RawPath only when a segment carries an escaped separator such
// as %2F. Every other request has the decoded path in Path already, and
// decoding twice would turn "%252F" into a separator the client never sent.
func requestPath(u *url.URL) (path string, escaped bool) {
	// EscapedPath rebuilds the escaped form byte by byte, which is a quarter of
	// a pooled request. It is only needed when the answer can differ from Path.
	// An empty RawPath means net/url reproduces the request target from Path,
	// and the canonical form decodes every escape it would add except the ones
	// standing for a backslash or a percent, so a Path holding neither is
	// already the answer.
	if u.RawPath == "" && strings.IndexByte(u.Path, '%') < 0 && strings.IndexByte(u.Path, '\\') < 0 {
		return u.Path, false
	}
	path = canonicalEscapedPath(u.EscapedPath())
	if path == u.Path {
		return u.Path, false
	}
	return path, true
}

func canonicalEscapedPath(path string) string {
	const hex = "0123456789ABCDEF"
	i := strings.IndexByte(path, '%')
	if i < 0 {
		return path
	}
	for ; i+2 < len(path); i++ {
		if path[i] != '%' {
			continue
		}
		v, ok := unhex(path[i+1], path[i+2])
		if !ok {
			continue
		}
		preserve := v == '/' || v == '\\' || v == '%'
		if preserve && path[i+1] == hex[v>>4] && path[i+2] == hex[v&15] {
			continue
		}
		out := make([]byte, 0, len(path))
		out = append(out, path[:i]...)
		for i < len(path) {
			if i+2 < len(path) && path[i] == '%' {
				if v, ok := unhex(path[i+1], path[i+2]); ok {
					if v == '/' || v == '\\' || v == '%' {
						out = append(out, '%', hex[v>>4], hex[v&15])
					} else {
						out = append(out, v)
					}
					i += 3
					continue
				}
			}
			out = append(out, path[i])
			i++
		}
		return string(out)
	}
	return path
}

func (r *Router[C]) dispatch(c C, h HandlerFunc[C]) error {
	err := h(c)
	if err != nil {
		r.handleError(c, err)
	}
	return err
}

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
	r.eng.errorHandlerFor(c.base())(c, err)
}

func (e *engine[C]) errorHandlerFor(b *Base) ErrorHandlerFunc[C] {
	if !b.errorRouted {
		return e.rootErrorHandler
	}
	if b.errorScopeIdx >= 0 && int(b.errorScopeIdx) < len(e.errScopes) {
		return e.errScopes[b.errorScopeIdx].errHandler
	}
	var host *hostEntry[C]
	if b.hostIdx >= 0 && e.hostSet != nil && int(b.hostIdx) < len(e.hostSet.all) {
		host = e.hostSet.all[b.hostIdx]
	}
	if host != nil && host.errHandler != nil {
		return host.errHandler
	}
	return e.rootErrorHandler
}

// selectErrorTarget picks the scope whose error handler owns this request. It
// runs while the routed path is still known: a handler is free to rewrite the
// request, and that must not change who handles its failure.
func (e *engine[C]) selectErrorTarget(b *Base, host *hostEntry[C], path string, escaped bool) {
	b.errorRouted = true
	b.errorScopeIdx = -1
	if s := scopeFor(e.errScopes, host, path, escaped); s != nil {
		b.errorScopeIdx = s.errorIdx
	}
	if host != nil {
		b.hostIdx = host.idx
	} else {
		b.hostIdx = -1
	}
}

func (e *engine[C]) canMatch(host *hostEntry[C], path, method string, scratch []string, escaped bool) bool {
	if host != nil {
		var st matchState[C]
		if n, _ := search(host.tree, path, method, scratch, &st, escaped); n != nil || st.pathMatch != nil {
			return true
		}
	}
	if !e.anyHostRoutes {
		return false
	}
	var st matchState[C]
	n, _ := search(e.tree, path, method, scratch, &st, escaped)
	return n != nil || st.pathMatch != nil
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
	http.Redirect(w, req, u.RequestURI(), status)
}
