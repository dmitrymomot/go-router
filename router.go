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

type Route struct {
	Method  string
	Pattern string

	Host string

	Name string

	Meta any
}

const MethodQuery = "QUERY"

// No request can spell it: net/http rejects a method that is not a token.
const anyMethod = "*"

type registration[C Context] struct {
	method  string
	pattern string
	handler HandlerFunc[C]
	mws     []Middleware[C]

	name string
	meta any
}

type Router[C Context] struct {
	root   *Router[C]
	prefix string
	mws    []Middleware[C]

	regs      []registration[C]
	children  []*Router[C]
	hasRoutes bool

	name     string
	meta     any
	tagged   bool
	nameUsed bool

	hosts []hostSpec

	inHost bool

	notFound         HandlerFunc[C]
	methodNotAllowed HandlerFunc[C]
	errHandler       ErrorHandlerFunc[C]

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

	anyHostRoutes bool

	ropts *routerOpts

	maxBody      int64
	maxMultipart int64
	strictBind   bool
	logger       *slog.Logger
	jsonOpts     []json.Options

	compiled atomic.Bool

	preChain HandlerFunc[C]

	observer func(c Context, status int, size int64, d time.Duration, err error)

	preMws     []Middleware[C]
	compileErr error

	scopes []*scopeFallback[C]

	// Allow header per node, joined at build time and never written again.
	allowCache map[*node[C]]string

	errScopes []*scopeFallback[C]

	named map[string]namedRoute
	info  map[routeKey]routeInfo

	owner *Router[C]
}

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
	for _, m := range methods {
		r.Handle(m, pattern, h, mws...)
	}
}

func (r *Router[C]) Use(mws ...Middleware[C]) {
	if r.hasRoutes {
		panic("router: Use must come before the routes of a scope; open a Group for later middleware")
	}
	if r.root.started.Load() {
		panic("router: cannot add middleware after the router started serving")
	}
	r.mws = append(r.mws, mws...)
}

func (r *Router[C]) newChild(prefix string, mws []Middleware[C]) *Router[C] {
	c := &Router[C]{root: r.root, prefix: prefix, mws: mws, inHost: r.inHost || len(r.hosts) > 0}
	r.children = append(r.children, c)
	return c
}

func (r *Router[C]) Group(fn func(g *Router[C])) *Router[C] {
	c := r.newChild("", nil)
	if fn != nil {
		fn(c)
	}
	return c
}

func (r *Router[C]) Route(prefix string, fn func(g *Router[C])) *Router[C] {
	c := r.newChild(prefix, nil)
	if fn != nil {
		fn(c)
	}
	return c
}

func (r *Router[C]) With(mws ...Middleware[C]) *Router[C] {
	c := r.newChild("", slices.Clone(mws))
	c.owner = r
	return c
}

func (r *Router[C]) Pre(mws ...Middleware[C]) {
	if r.root != r {
		panic("router: Pre belongs to the root router, because it runs before matching picks a scope")
	}
	r.mustNotBeServing("the pre-routing middleware")
	r.preMws = append(r.preMws, mws...)
}

func (r *Router[C]) Name(name string) *Router[C] {
	if name == "" {
		panic("router: Name needs a name")
	}
	c := r.tag()
	c.name = name
	return c
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
	c.tagged, c.owner = true, r
	return c
}

func (r *Router[C]) Host(pattern string, fn func(h *Router[C])) *Router[C] {
	return r.Hosts([]string{pattern}, fn)
}

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
	shim := r.newChild(prefix, nil)
	shim.children = append(shim.children, sub)
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
		req := stripMountPrefix(b.req, b.rawTail, b.pathEscaped)
		b.SetRequest(req)
		h.ServeHTTP(b.res, req)
		return nil
	}
	r.Any(prefix, handler)
	r.Any(joinPattern(prefix, "/{"+mountParam+"...}"), handler)
}

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

func (r *Router[C]) mustNotBeServing(what string) {
	if r.root.started.Load() {
		panic("router: cannot change " + what + " after the router started serving")
	}
}

func (r *Router[C]) NotFound(h HandlerFunc[C]) {
	r.mustNotBeServing("the not-found handler")
	r.notFound = h
}

func (r *Router[C]) MethodNotAllowed(h HandlerFunc[C]) {
	r.mustNotBeServing("the method-not-allowed handler")
	r.methodNotAllowed = h
}

func (r *Router[C]) ErrorHandler(h ErrorHandlerFunc[C]) {
	r.mustNotBeServing("the error handler")
	r.errHandler = h
}

func (r *Router[C]) HandleOPTIONS(on bool) {
	r.mustNotBeServing("the OPTIONS setting")
	r.root.autoOptions = on
}

func (r *Router[C]) MaxBodyBytes(n int64) {
	r.mustNotBeServing("the body limit")
	r.root.maxBody = n
}

func (r *Router[C]) MaxMultipartMemory(n int64) {
	r.mustNotBeServing("the multipart memory limit")
	r.root.maxMultipart = n
}

func (r *Router[C]) StrictBind(on bool) {
	r.mustNotBeServing("the strict binding setting")
	r.root.strictBind = on
}

func (r *Router[C]) Logger(l *slog.Logger) {
	r.mustNotBeServing("the logger")
	r.root.logger = l
}

func (r *Router[C]) JSONOptions(opts ...json.Options) {
	r.mustNotBeServing("the JSON options")
	r.root.jsonOpts = opts
}

func (r *Router[C]) RedirectTrailingSlash(on bool) {
	r.mustNotBeServing("the trailing slash setting")
	r.root.redirectSlash = on
}

func (r *Router[C]) Observe(fn func(c Context, status int, size int64, d time.Duration, err error)) {
	r.mustNotBeServing("the observer")
	r.root.observer = fn
}

func (r *Router[C]) Routes() []Route {
	root := r.root
	root.build()
	return slices.Clone(root.routes)
}

func (r *Router[C]) Build() error { return r.root.compile() }

func (r *Router[C]) compile() error {
	r.once.Do(func() { r.compileErr = r.buildErr() })
	return r.compileErr
}

func (r *Router[C]) build() {
	if err := r.compile(); err != nil {
		panic(err.Error())
	}
}

func (r *Router[C]) buildErr() error {
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

		handlers := make([]HandlerFunc[C], len(rt.regs))
		for i, reg := range rt.regs {
			handlers[i] = chain(reg.handler, concatMiddleware(m, reg.mws))
		}

		rounds := max(len(rt.hosts), 1)
		for i := range rounds {
			e := host
			if len(rt.hosts) > 0 {
				if host != nil {
					return errors.New("router: a host scope cannot sit inside another host scope")
				}
				e = r.hostEntry(rt.hosts[i])
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

			own := inherited
			switch {
			case rt != r && rt.root == rt:

			case len(rt.hosts) > 0 || normalizePattern(p) == "/":
				notFound, notAllowed, errh := r.fallbacks(e)
				if rt.notFound != nil {
					*notFound = chain(rt.notFound, m)
					if e == nil {
						r.notFound = rt.notFound
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
	r.allowCache = map[*node[C]]string{}
	r.tree.cacheAllow(r.allowCache)
	if r.hostSet != nil {
		for _, e := range r.hostSet.all {
			e.tree.cacheAllow(r.allowCache)
		}
	}
	if len(r.preMws) > 0 {
		r.preChain = chain(r.preTerminal, r.preMws)
	}
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

func (r *Router[C]) describe(reg registration[C], e *hostEntry[C], pattern string) error {
	if reg.name == "" && reg.meta == nil {
		return nil
	}
	host := ""
	if e != nil {
		host = e.pattern
	}
	if reg.name != "" {
		if prev, ok := r.named[reg.name]; ok && prev.pattern != pattern {
			return fmt.Errorf("router: the route name %q names both %q and %q", reg.name, prev.pattern, pattern)
		}
		if r.named == nil {
			r.named = make(map[string]namedRoute)
		}
		nr := namedRoute{pattern: pattern, parts: parseURLTemplate(pattern)}
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

func (r *Router[C]) preTerminal(c C) error { return r.route(c, c.base().req) }

type pendingScope[C Context] struct {
	prefix string
	host   *hostEntry[C]
	mws    []Middleware[C]

	fb    scopeFallbacks[C]
	depth int
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
	prefix string

	segs    int
	statics int
	depth   int

	hostIdx int32

	notFoundChain   HandlerFunc[C]
	notAllowedChain HandlerFunc[C]
	optionsChain    HandlerFunc[C]

	errHandler ErrorHandlerFunc[C]
}

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

func segmentCovers(want, got string) bool {
	if want == "*" || strings.IndexByte(want, '{') >= 0 {
		return true
	}
	return want == got
}

func cutSegment(p string) (seg, rest string) {
	p = p[1:]
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], p[i:]
	}
	return p, ""
}

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

func (r *Router[C]) fallbackChains(host *hostEntry[C], path string) (notFound, notAllowed, options HandlerFunc[C]) {
	if s := scopeFor(r.scopes, host, path); s != nil {
		return s.notFoundChain, s.notAllowedChain, s.optionsChain
	}
	if host != nil {
		return host.notFoundChain, host.notAllowedChain, host.optionsChain
	}
	return r.notFoundChain, r.notAllowedChain, r.optionsChain
}

func autoOptions[C Context](c C) error { return c.base().NoContent(http.StatusNoContent) }

func (r *Router[C]) fallbacks(e *hostEntry[C]) (notFound, notAllowed *HandlerFunc[C], errh *ErrorHandlerFunc[C]) {
	if e != nil {
		return &e.notFoundChain, &e.notAllowedChain, &e.errHandler
	}
	return &r.notFoundChain, &r.notAllowedChain, &r.errHandler
}

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

type routeKey struct{ host, method, pattern string }

type routeInfo struct {
	name string
	meta any
}

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
		root.dispatch(c, root.preChain)
		return
	}
	//nolint:errcheck // Same as above.
	root.route(c, req)
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
	err = r.route(c, req)
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
		r.reset(c)
		r.pool.Put(c)
	}
}

func (r *Router[C]) recycle(c C) {
	if r.pool != nil {
		r.reset(c)
		r.pool.Put(c)
	}
}

func (r *Router[C]) route(c C, req *http.Request) error {
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

	var (
		host     *hostEntry[C]
		hostVals = b.paramArr[:0]
	)
	if r.hostSet != nil {
		b.host, b.hostKnown = normalizeHost(req.Host), true
		host, hostVals = r.hostSet.match(b.host, hostVals)
		if host != nil {
			b.hostIdx, b.hostPattern = host.idx, host.pattern
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
	if n == nil && r.anyHostRoutes {
		if m, v := search(r.tree, trimmed, req.Method, hostVals[len(hostVals):], &anySt); m != nil {
			n, vals, nHost = m, v, 0
			b.hostIdx, b.hostPattern = -1, ""
		}
	}

	switch {
	case n != nil:
		if n.kind == edgeWildcard && len(vals) > 0 {
			b.rawTail = vals[len(vals)-1]
		}
		if escaped {
			unescapeParams(vals[nHost:])
		}
		b.setRoute(n.pattern, n.names, vals)
		req.Pattern = n.pattern
		return r.dispatch(c, n.handler(req.Method))

	case hostSt.pathMatch != nil || anySt.pathMatch != nil:
		match, matched, skip := hostSt.pathMatch, hostSt.pathVals, len(hostVals)
		if match == nil {
			match, matched, skip = anySt.pathMatch, anySt.pathVals, 0
			host = nil
			b.hostIdx, b.hostPattern = -1, ""
		}
		if escaped {
			unescapeParams(matched[skip:])
		}
		b.setRoute(match.pattern, match.names, matched)
		req.Pattern = match.pattern
		b.res.Header().Set(HeaderAllow, r.allowHeader(&hostSt, &anySt))

		_, notAllowed, options := r.fallbackChains(host, trimmed)
		if req.Method == http.MethodOptions && r.autoOptions {
			return r.dispatch(c, options)
		}
		return r.dispatch(c, notAllowed)

	default:
		notFound, _, _ := r.fallbackChains(host, trimmed)
		return r.dispatch(c, notFound)
	}
}

func (r *Router[C]) allowHeader(host, anyHost *matchState[C]) string {
	if host.rest == nil && anyHost.rest == nil {
		var only *node[C]
		switch {
		case host.pathMatch != nil && anyHost.pathMatch == nil:
			only = host.pathMatch
		case anyHost.pathMatch != nil && host.pathMatch == nil:
			only = anyHost.pathMatch
		}
		if only != nil {
			if s, ok := r.allowCache[only]; ok {
				return s
			}
		}
	}
	out := host.allowedMethods(nil)
	out = anyHost.allowedMethods(out)
	slices.Sort(out)
	return strings.Join(out, ", ")
}

// net/url fills RawPath only when a segment carries an escaped separator such
// as %2F. Every other request has the decoded path in Path already, and
// decoding twice would turn "%252F" into a separator the client never sent.
func requestPath(u *url.URL) (path string, escaped bool) {
	if u.RawPath != "" {
		return u.RawPath, true
	}
	return u.Path, false
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
	r.errorHandlerFor(c.base())(c, err)
}

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

func (r *Router[C]) scopeErrorHandler(host *hostEntry[C], u *url.URL) ErrorHandlerFunc[C] {
	path, _ := requestPath(u)
	if s := scopeFor(r.errScopes, host, normalizePattern(path)); s != nil {
		return s.errHandler
	}
	return nil
}

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
