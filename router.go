package router

import (
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// MethodQuery is the QUERY method of RFC 9110. The standard library has no
// constant for it.
const MethodQuery = "QUERY"

// No request can spell it: net/http rejects a method that is not a token.
const anyMethod = "*"

type registration[C Context] struct {
	method  string
	pattern string
	handler HandlerFunc[C]
	mws     []Middleware[C]
	// claim is set on the routes of a RedirectHost, which own their host in
	// every table they are installed into, a Mount replay included.
	claim *hostClaim
	// rest appends a catch-all under mountParam, a name no pattern can spell,
	// so MountHandler and RedirectHost take the rest of the path.
	rest bool
}

// hostClaim is the hold one RedirectHost call has on its host. It is not zero
// size, so two claims never share an address.
type hostClaim struct{ _ byte }

// Router matches a request to a handler. C is the context type of the
// application, and every handler and middleware of this router takes it.
//
// Build one with [New] or [NewPooled], register routes, then serve it: Router
// is an [http.Handler]. A setter panics once the first request arrives, and
// every setter leaves the router ready to serve, so there is no build step to
// forget.
type Router[C Context] struct {
	// The request path reads this block on every request, so it stays together
	// and in front: ServeHTTP walks started, observer, pool and preChain before
	// route touches the trie and the fallbacks.
	root     *Router[C]
	started  atomic.Bool
	observer func(c C, status int, size int64, d time.Duration, err error)
	pool     *sync.Pool
	preChain HandlerFunc[C]
	newCtx   func(http.ResponseWriter, *http.Request) C
	reset    func(C)
	ropts    *routerOpts

	// eng is the compiled route table. Only the root holds one; every scope
	// reaches it through root.
	eng *engine[C]

	// regMu orders every setter against the one-time freeze, so the check that
	// refuses a late change cannot be overtaken by the request that makes it
	// late. No request ever takes it.
	regMu sync.Mutex

	// Registration only, from here down.
	prefix       string
	mws          []Middleware[C]
	regs         []registration[C]
	children     []*Router[C]
	owner        *Router[C]
	hasRoutes    bool
	closed       bool
	refreshDepth int
	meta         []any
	hosts        []hostSpec
	inHost       bool
	errHandler   ErrorHandlerFunc[C]
	mounted      *Router[C] // the router a Mount shim holds
	preMws       []Middleware[C]
	classes      map[string]*matcher
}

// engine is the route table as the request path sees it: everything routing
// reads, and nothing registration keeps.
type engine[C Context] struct {
	// owner dispatches errors and owns the context pool.
	owner   *Router[C]
	tree    *node[C]
	hostSet *hostSet[C]

	allowCache      map[*node[C]]string
	scopes          []*scopeFallback[C]
	notFoundChain   HandlerFunc[C]
	notAllowedChain HandlerFunc[C]
	optionsChain    HandlerFunc[C]
	// errHandlers holds every error handler the table uses; index 0 is the
	// root's. Base.errIdx picks one per request.
	errHandlers   []ErrorHandlerFunc[C]
	autoOptions   bool
	redirectSlash bool
	anyHostRoutes bool

	// Registration only: the owner index of each scope on each tree, as the
	// routes read it, and the value compile resolved for it.
	errSlots    map[errKey[C]]*int32
	errResolved map[errKey[C]]int32
}

// errKey names a scope on one tree: host is nil for the tree of every host.
type errKey[C Context] struct {
	scope *Router[C]
	host  *hostEntry[C]
}

func newEngine[C Context]() *engine[C] {
	return &engine[C]{
		tree:        new(node[C]),
		allowCache:  map[*node[C]]string{},
		autoOptions: true,
		// A router that never calls a setter still has to answer.
		notFoundChain:   defaultNotFound[C],
		notAllowedChain: defaultMethodNotAllowed[C],
		optionsChain:    autoOptions[C],
		errHandlers:     []ErrorHandlerFunc[C]{TextErrorHandler[C](false)},
		errSlots:        map[errKey[C]]*int32{},
	}
}

// New builds a router that calls newContext once per request. The returned
// context must embed [Base]; the router fills it before the handler runs.
//
// New panics if newContext is nil.
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
	r.ropts.answer = func(c Context, err error) {
		cc, ok := c.(C)
		if !ok {
			panic(fmt.Sprintf("router: HandleError got a %T, which is not the context type of the router that serves it", c))
		}
		r.handleError(cc, err)
	}
	return r
}

// NewPooled builds a router that reuses its contexts. newContext builds one,
// and reset clears the fields of the application before the context goes back
// to the pool. A pooled context belongs to one request, so a handler must not
// keep it past the point where it returns.
//
// NewPooled panics if newContext or reset is nil.
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

// Handle registers h for one method and pattern. mws wrap this route alone,
// inside whatever [Router.Use] already added.
//
// A pattern names a parameter in braces, "/users/{id}", and a trailing
// "{name...}" takes the rest of the path. A parameter may sit inside a
// segment, as in "/reports/rep-{date}.csv".
//
// A constraint after a colon narrows what a parameter admits, and a value
// outside it does not match the route. A word names a class: "{id:int}" takes
// ASCII digits, "{t:slug}" lowercase letters, digits and inner hyphens, and
// "{id:uuid}" the canonical 8-4-4-4-12 form of a UUID in either case, and no
// other spelling of one. Any other word must name a class that
// [Router.ParamClass] declares; a literal word is spelled "(?:word)". Anything
// else is a regular expression that must match the whole value, as in
// "{id:[0-9]{6}}".
//
// Handle panics on an empty method, a nil handler, a nil middleware, a
// constraint that names no class or does not compile, a pattern that conflicts
// with one already registered, or a call that arrives after the router started
// serving.
func (r *Router[C]) Handle(method, pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.register(registration[C]{method: method, pattern: pattern, handler: h, mws: mws})
}

func (r *Router[C]) register(reg registration[C]) {
	method, pattern := reg.method, reg.pattern
	// Ordered against the freeze: without it the check below is a guess, and a
	// route could go into a trie a request had already started reading.
	defer r.guard("register " + method + " " + pattern)()
	if method == "" {
		panic("router: Handle needs a method")
	}
	if reg.handler == nil {
		panic("router: Handle needs a handler for " + method + " " + pattern)
	}
	validateMiddleware(reg.mws)
	for s := r; s != nil; s = s.owner {
		s.hasRoutes = true
	}
	reg.mws = slices.Clone(reg.mws)
	r.regs = append(r.regs, reg)
	r.install(reg)
}

// install puts the route in its trie now, so a malformed pattern or a conflict
// panics at the line that wrote it rather than at the first request.
func (r *Router[C]) install(reg registration[C]) {
	eng := r.top().eng
	full := joinPattern(r.scopePrefix(), reg.pattern)
	segs, names, err := r.parseScoped(reg.pattern)
	if err != nil {
		panic(err.Error())
	}
	if reg.rest {
		full = joinPattern(full, mountRest)
		if n := len(segs); n > 0 && segs[n-1].kind == segWildcard {
			panic(fmt.Sprintf("router: catch-all must be the last segment in %q", full))
		}
		segs = append(segs, segment{kind: segWildcard, value: mountParam})
		names = append(names, mountParam)
	}
	handler := chain(reg.handler, concatMiddleware(r.scopeMiddleware(), reg.mws))
	// Read here rather than at registration, so a route that Mount replays
	// takes the Meta scopes above the mount too.
	meta := r.scopeMeta()
	if len(meta) > 0 {
		handler = withRouteMeta(handler, &routeRecord{pattern: normalizePattern(full), meta: meta})
	}
	mh := methodHandler[C]{method: reg.method, handler: handler, meta: meta}

	entries := r.hostEntriesIn(eng)
	if len(entries) == 0 {
		eng.anyHostRoutes = true
		mh.errIdx = r.errSlot(eng, nil)
		if err := eng.tree.insert(full, segs, names, nil, mh, eng.autoOptions, eng.allowCache); err != nil {
			panic(err.Error())
		}
		return
	}
	if reg.claim != nil && normalizePattern(r.scopePrefix()) != "/" {
		panic(errRedirectHostPrefix)
	}
	for _, e := range entries {
		claim := reg.claim != nil && e.redirect != reg.claim
		switch {
		case reg.claim == nil && e.redirect != nil:
			panic("router: the host " + e.pattern + " belongs to RedirectHost; register its routes on the target host")
		case claim && e.redirect != nil:
			panic("router: the host " + e.pattern + " already has a RedirectHost")
		case claim && !e.tree.empty():
			panic("router: the host " + e.pattern + " already holds routes, so RedirectHost cannot own it")
		}
		mh.errIdx = r.errSlot(eng, e)
		if err := e.tree.insert(full, segs, names, e.names, mh, eng.autoOptions, eng.allowCache); err != nil {
			panic(err.Error())
		}
		if claim {
			e.redirect = reg.claim
		}
	}
}

// GET registers h for GET. See [Router.Handle].
func (r *Router[C]) GET(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodGet, pattern, h, mws...)
}

// HEAD registers h for HEAD. A GET route answers HEAD on its own, so this is
// only for a HEAD that differs. See [Router.Handle].
func (r *Router[C]) HEAD(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodHead, pattern, h, mws...)
}

// POST registers h for POST. See [Router.Handle].
func (r *Router[C]) POST(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodPost, pattern, h, mws...)
}

// PUT registers h for PUT. See [Router.Handle].
func (r *Router[C]) PUT(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodPut, pattern, h, mws...)
}

// PATCH registers h for PATCH. See [Router.Handle].
func (r *Router[C]) PATCH(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodPatch, pattern, h, mws...)
}

// DELETE registers h for DELETE. See [Router.Handle].
func (r *Router[C]) DELETE(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodDelete, pattern, h, mws...)
}

// OPTIONS registers h for OPTIONS, in place of the answer that
// [Router.HandleOPTIONS] builds. See [Router.Handle].
func (r *Router[C]) OPTIONS(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(http.MethodOptions, pattern, h, mws...)
}

// Any registers h for every method. A route registered for one method wins
// over this one on that method. See [Router.Handle].
func (r *Router[C]) Any(pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	r.Handle(anyMethod, pattern, h, mws...)
}

// Match registers h for each of methods. See [Router.Handle].
//
// Match panics on an empty methods, and where Handle panics.
func (r *Router[C]) Match(methods []string, pattern string, h HandlerFunc[C], mws ...Middleware[C]) {
	// Without this the call registers nothing and says nothing, and the route
	// is missing at the first request instead of at the line that wrote it.
	if len(methods) == 0 {
		panic("router: Match needs at least one method for " + pattern)
	}
	for _, m := range methods {
		r.Handle(m, pattern, h, mws...)
	}
}

// Pre adds middleware that runs before matching, so it also covers the
// requests that end in a 404 or a 405. A pre-routing middleware sees no route
// pattern and no route parameter.
//
// Pre panics on a scope, on a nil middleware, or after the router started
// serving.
func (r *Router[C]) Pre(mws ...Middleware[C]) {
	r.mustBeRoot("Pre", "it runs before matching picks a scope")
	defer r.guard("change the pre-routing middleware")()
	validateMiddleware(mws)
	r.preMws = append(r.preMws, mws...)
	r.settingChanged()
}

// ParamClass declares a class of parameter values that a pattern names after
// a colon, as in "/r/{code:shortid}". A value outside the class does not match
// the route, so matching moves on, and a request that matches nothing else
// ends in a 404. The class applies to every pattern this router registers,
// including host patterns and scope prefixes, from the call on.
//
// match receives the decoded value, never an empty one. It runs on every
// request that reaches the segment, so it must be fast and safe for concurrent
// use. The MatchString of a regular expression anchored with ^ and $ works.
// A router that [Router.Mount] grafts keeps the classes it declared for its own
// patterns, and the prefix it is mounted under keeps the classes of the parent.
//
// ParamClass panics on a scope; on a name that does not start with an ASCII
// letter or holds anything but letters, digits and '_'; on a built-in or
// already declared name; on a nil match; on a mounted router; or after the
// router started serving.
func (r *Router[C]) ParamClass(name string, match func(value string) bool) {
	r.mustBeRoot("ParamClass", "a class applies to all of its patterns")
	defer r.guard("change the parameter classes")()
	if !validClassName(name) {
		panic(fmt.Sprintf("router: ParamClass needs a name of letters, digits and '_' that starts with a letter, not %q", name))
	}
	if builtinClass(name) != nil {
		panic("router: ParamClass cannot redeclare the built-in class " + name)
	}
	if _, ok := r.classes[name]; ok {
		panic("router: ParamClass got the class " + name + " twice")
	}
	if match == nil {
		panic("router: ParamClass needs a match function for " + name)
	}
	if r.classes == nil {
		r.classes = make(map[string]*matcher)
	}
	// The key tells this declaration from a same-named one of another router,
	// and orders host patterns the same way on every run.
	r.classes[name] = &matcher{
		key:   fmt.Sprintf("class %s#%016x", name, classSeq.Add(1)),
		match: func(s string) bool { return s != "" && match(s) },
	}
}

var classSeq atomic.Uint64

// class finds a class from the scope that registers outward, so a mounted
// router finds its own classes before those of the router it is mounted into.
func (r *Router[C]) class(name string) *matcher {
	for s := r; s != nil; s = s.owner {
		if m, ok := s.classes[name]; ok {
			return m
		}
	}
	return builtinClass(name)
}

// guard takes the lock that orders setup against the first request, checks
// that the router may still change, and returns the unlock. A setter defers it
// around its whole change, so a request never reads a half-made one. what
// completes "cannot ..." in the panic, as in "change the logger".
func (r *Router[C]) guard(what string) (unlock func()) {
	root := r.root
	root.regMu.Lock()
	if root.started.Load() {
		root.regMu.Unlock()
		panic("router: cannot " + what + " after the router started serving")
	}
	if r.closed {
		root.regMu.Unlock()
		panic("router: cannot " + what + " on a mounted router; do it before Mount")
	}
	return root.regMu.Unlock
}

// mustBeRoot rejects a setting that applies to the whole router when a scope
// calls it, since a scope would silently change the root.
func (r *Router[C]) mustBeRoot(setter, why string) {
	if r.root != r {
		panic("router: " + setter + " belongs to the root router, because " + why)
	}
}

// ErrorHandler installs the handler that answers a request whose handler
// returned an error. Any scope may hold one. It answers the routes that the
// scope and the scopes inside it register, unless a nearer scope holds its own,
// so an API host takes [JSONErrorHandler] rather than one handler branching on
// the host. A route outside every scope with a handler goes to the handler of
// its host scope, then to the router's, which is [TextErrorHandler] with
// exposeCause unset until the router calls ErrorHandler.
//
// A 404 or 405 has no route. It goes to the handler that the routes of the
// most specific scope with a prefix covering the path would get, then to the
// one of its host scope, then to the router's. So a [Router.Group] or
// [Router.With] scope answers the errors of its routes, and never a 404 on its
// own. A router mounted under a prefix answers the 404s under it.
//
// The router logs every failure itself, skips h for a response that already
// committed and for an error that is [context.Canceled], and answers a bare
// 500 when h returns an error having written nothing. To give a domain error
// its status, make it a [StatusCoder] rather than mapping it inside h.
//
// ErrorHandler panics if h is nil, on a host scope whose host already has a
// handler from another Host or Hosts scope, on a mounted router, or after the
// router started serving.
func (r *Router[C]) ErrorHandler(h ErrorHandlerFunc[C]) {
	if h == nil {
		panic("router: ErrorHandler needs a handler")
	}
	defer r.guard("change the error handler")()
	r.errHandler = h
	r.settingChanged()
}

// HandleOPTIONS decides whether the router answers OPTIONS itself with the
// methods of the matched path. It is on by default. An OPTIONS route of your
// own wins either way.
//
// HandleOPTIONS panics on a scope, on a mounted router, or after the router started
// serving.
func (r *Router[C]) HandleOPTIONS(on bool) {
	r.mustBeRoot("HandleOPTIONS", "it applies to the whole router")
	defer r.guard("change the OPTIONS setting")()
	root := r.root
	root.eng.autoOptions = on
	root.eng.tree.recacheAllow(on, root.eng.allowCache)
	if root.eng.hostSet != nil {
		for _, e := range root.eng.hostSet.all {
			e.tree.recacheAllow(on, root.eng.allowCache)
		}
	}
}

// MaxBodyBytes caps the request body that the Bind methods and the form
// readers read. It defaults to [DefaultMaxBodyBytes], and a body over the cap
// fails with [ErrPayloadTooLarge]. A value of zero or less lifts the cap.
// [Base.SetBodyLimit], and so the BodyLimit middleware, replaces it for one
// request, above or below.
//
// MaxBodyBytes panics on a scope, on a mounted router, or after the router started
// serving.
func (r *Router[C]) MaxBodyBytes(n int64) {
	r.mustBeRoot("MaxBodyBytes", "it applies to the whole router")
	defer r.guard("change the body limit")()
	r.root.ropts.maxBody = n
}

// MaxMultipartMemory caps the memory that a multipart form takes before its
// parts spill to a temporary file. Zero takes the default of net/http.
//
// MaxMultipartMemory panics on a scope, on a mounted router, or after the router started
// serving.
func (r *Router[C]) MaxMultipartMemory(n int64) {
	r.mustBeRoot("MaxMultipartMemory", "it applies to the whole router")
	defer r.guard("change the multipart memory limit")()
	r.root.ropts.maxMultipart = n
}

// Logger installs the logger that [Base.Logger] and the log of failed requests
// use. A nil logger takes [slog.Default]. To quiet the log of failed requests,
// pass a logger whose handler drops the levels you do not want.
//
// Logger panics on a scope, on a mounted router, or after the router started
// serving.
func (r *Router[C]) Logger(l *slog.Logger) {
	r.mustBeRoot("Logger", "it applies to the whole router")
	defer r.guard("change the logger")()
	r.root.ropts.logger = l
}

// JSONOptions sets the options that [Base.JSON], [Base.BindJSON] and the
// SendJSON of package sse apply. The options of a single call win over these.
//
// JSONOptions panics on a scope, on a mounted router, or after the router started
// serving.
func (r *Router[C]) JSONOptions(opts ...json.Options) {
	r.mustBeRoot("JSONOptions", "it applies to the whole router")
	defer r.guard("change the JSON options")()
	r.root.ropts.jsonOpts = slices.Clone(opts)
}

// RedirectTrailingSlash decides whether a request whose path differs from a
// route by a trailing slash gets a redirect to the route: 301 for GET and
// HEAD, 308 for anything else. It is off by default, and such a request
// otherwise ends in a 404.
//
// RedirectTrailingSlash panics on a scope, on a mounted router, or after the router started
// serving.
func (r *Router[C]) RedirectTrailingSlash(on bool) {
	r.mustBeRoot("RedirectTrailingSlash", "it applies to the whole router")
	defer r.guard("change the trailing slash setting")()
	r.root.eng.redirectSlash = on
}

// Observe installs a function that runs once per request, after the response
// is written, with the status, the size of the body, how long the request
// took, and the error the handler returned. It suits a metric; a log line
// belongs in a middleware, which can also read the request.
//
// Observe panics on a scope, if fn is nil, on a mounted router, or after the
// router started serving.
func (r *Router[C]) Observe(fn func(c C, status int, size int64, d time.Duration, err error)) {
	r.mustBeRoot("Observe", "it applies to the whole router")
	if fn == nil {
		panic("router: Observe needs a function")
	}
	defer r.guard("change the observer")()
	r.root.observer = fn
}
