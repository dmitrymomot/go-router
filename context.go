// Package router implements an HTTP router whose handlers take an
// application-defined request context.
//
// A handler is func(C) error, where C embeds [Base]. The router fills the
// embedded Base before it calls the handler.
package router

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Context is what every handler receives. An application declares a struct
// that embeds [Base] and adds the fields it needs, and that struct satisfies
// Context. The unexported method keeps the set of implementations to the types
// that embed Base.
type Context interface {
	context.Context
	Request() *http.Request
	SetRequest(r *http.Request)
	SetContext(ctx context.Context)
	SetBodyLimit(n int64)
	Response() *Response
	Set(key string, value any)
	Get(key string) (any, bool)
	Param(name string) string
	RoutePattern() string
	RouteMeta() []any
	Host() string
	RouteHost() string

	base() *Base
}

// Route parameters live inside Base up to this many, so a request that stays
// under it answers without a second allocation. Host parameters count too: a
// "{tenant}.example.com" scope spends one before the path spends any. Going
// over is not an error, it costs one allocation per request, and on a pooled
// router that is the difference between zero and one. Route.Params and
// InlineParamBudget let a route table assert it stays under.
const maxInlineParams = 4

// Base carries the request, the response and the route. An application
// context embeds it, and the router fills it before each handler call.
//
// A Base belongs to one request. A pooled router hands it to the next request
// once the handler returns, so a handler must not keep it past that point.
//
//betteralign:check
type Base struct {
	req         *http.Request
	res         *Response
	store       map[string]any
	paramArr    [maxInlineParams]string
	queryCache  url.Values
	host        string
	hostPattern string
	rawTail     string
	paramNames  []string
	paramVals   []string
	ropts       *routerOpts
	route       *routeRecord

	// One word rather than the fields it points to, which would push an
	// embedder with a string of its own past 320 bytes and into the next size
	// class.
	deferred   *deferredState
	resStorage Response
	// errIdx picks the error handler, in engine.errHandlers, that answers a
	// failure of this request. 0, the root's, until routing picks one.
	errIdx       int32
	hostKnown    bool
	pathEscaped  bool
	needsCleanup bool
	errorHandled bool

	// Routing matches the path trimmed of its trailing slash, so a mounted
	// handler has to be told the slash was there.
	tailSlash bool
}

type routerOpts struct {
	jsonOpts     []json.Options
	logger       *slog.Logger
	answer       func(c Context, err error) // HandleError's way to the router that serves
	maxBody      int64
	maxMultipart int64
}

var defaultRouterOpts = &routerOpts{maxBody: DefaultMaxBodyBytes}

func (b *Base) opts() *routerOpts {
	if b.ropts == nil {
		return defaultRouterOpts
	}
	return b.ropts
}

// NewBase builds a Base outside a router, for a test or for a handler that the
// router never calls. The route, its parameters and the host stay empty.
//
// To test a handler of your own context type, let routertest.NewContext build
// the whole context. It fills an embedded Base in place, whereas a struct
// literal that copies *NewBase(w, r) keeps writing through the Base it was
// copied from.
//
// NewBase panics if w or r is nil.
func NewBase(w http.ResponseWriter, r *http.Request) *Base {
	if w == nil {
		panic("router: NewBase needs a response writer")
	}
	if r == nil {
		panic("router: NewBase needs a request")
	}
	b := new(Base)
	b.init(w, r)
	return b
}

// Every request pays for this, so keep it to plain stores.
func (b *Base) init(w http.ResponseWriter, r *http.Request) {
	res, ok := w.(*Response)
	if !ok {
		b.resStorage = Response{ResponseWriter: w}
		res = &b.resStorage
	}
	b.req, b.res = r, res
	b.route, b.rawTail = nil, ""
	b.paramNames, b.paramVals = nil, b.paramArr[:0]
	b.host, b.hostKnown, b.hostPattern = "", false, ""
	b.errIdx = 0
	b.pathEscaped, b.tailSlash = false, false
	b.queryCache = nil
	b.deferred = nil
	// clear on a map is a runtime call even when the map is nil, and most
	// requests never set anything.
	if len(b.store) > 0 {
		clear(b.store)
	}
}

func (b *Base) clearRequestSlow() {
	b.resStorage.before = nil
	clear(b.paramArr[:])
	clear(b.paramVals[:cap(b.paramVals)])
	b.paramVals = nil
	b.queryCache = nil
	if len(b.store) > 0 {
		clear(b.store)
	}
	b.deferred = nil
	b.host, b.rawTail = "", ""
	b.needsCleanup = false
}

// deferredState holds what few requests need, so Base does not carry it.
type deferredState struct {
	form error
	// bodyLimit is the cap of SetBodyLimit: 0 leaves the router's, and -1
	// lifts it.
	bodyLimit int64
}

func (b *Base) deferrals() *deferredState {
	if b.deferred == nil {
		b.deferred = new(deferredState)
		b.needsCleanup = true
	}
	return b.deferred
}

func (b *Base) formError() error {
	if b.deferred == nil {
		return nil
	}
	return b.deferred.form
}

func (b *Base) setFormError(err error) error {
	b.deferrals().form = err
	return err
}

// routeRecord is what a matched route publishes to its Base. Registration
// builds it, and every request of the route shares it, so it never changes.
// Base holds one pointer to it rather than the pattern string, which keeps an
// embedder with a string of its own in the 320-byte size class.
type routeRecord struct {
	pattern string
	meta    []any
}

func (b *Base) setRoute(rec *routeRecord, names, vals []string) {
	b.route = rec
	b.paramNames = names
	b.paramVals = vals
}

func (b *Base) base() *Base { return b }

// Request reports the request that the handler answers.
func (b *Base) Request() *http.Request { return b.req }

// SetRequest replaces the request. A middleware calls it after it wraps the
// body or rewrites the URL; to change the request context alone, it calls
// [Base.SetContext]. The cached query and host are dropped, so the next read
// takes them from r.
//
// SetRequest panics if r is nil, or if the context of r derives from b.
func (b *Base) SetRequest(r *http.Request) {
	if r == nil {
		panic("router: SetRequest needs a request")
	}
	// The request context in place already passed this check, or came from
	// net/http, so a copy of the request that keeps it, as RealIP and a mount
	// make, skips the walk up the chain.
	if ctx := r.Context(); ctx != b.req.Context() {
		b.mustNotBeAncestorOf(ctx, "SetRequest")
	}
	b.req = r
	b.queryCache = nil
	b.host, b.hostKnown = "", false
}

// SetContext replaces the context of the request, for a middleware that adds
// a value or a deadline. It copies the request with WithContext and keeps the
// cached query and host.
//
// A context derived from b is fine to pass on, to a [Component] or to domain
// code, but it must not come back as the request context: b looks up in the
// request context what it lacks itself. Derive ctx from Request().Context(),
// so that it still ends when the client goes away and the files that
// [Base.Bind] spilled to disk are still removed with the request.
//
// SetContext panics if ctx is nil or derives from b.
func (b *Base) SetContext(ctx context.Context) {
	if ctx == nil {
		panic("router: SetContext needs a context")
	}
	b.mustNotBeAncestorOf(ctx, "SetContext")
	b.req = b.req.WithContext(ctx)
}

// mustNotBeAncestorOf refuses a request context that looks up in b, since b
// looks up in the request context and the two would recurse until the stack
// runs out. The lookup stops at the first Base it meets.
func (b *Base) mustNotBeAncestorOf(ctx context.Context, what string) {
	if ctx.Value(baseKeyType{}) == b {
		panic("router: " + what + " got a context derived from the handler context itself; derive it from Request().Context()")
	}
}

// Logger reports the logger of the router, or [slog.Default] when the router
// has none.
func (b *Base) Logger() *slog.Logger {
	if l := b.opts().logger; l != nil {
		return l
	}
	return slog.Default()
}

// Response reports the response writer, which records the status and the
// number of bytes written. It is an [http.ResponseWriter], so a library that
// takes one takes it as it stands.
func (b *Base) Response() *Response { return b.res }

// releasedRequest stands in for the request once the handler has returned, so a
// Base held past its request reads as a finished context rather than
// dereferencing nil. Holding one is still a mistake: on a pooled router its
// values belong to whoever has it next.
var releasedRequest = func() *http.Request {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// URL and Header are filled in: Path, URL and Header reach through them,
	// and a nil one would dereference exactly where this is meant to stop.
	return (&http.Request{URL: new(url.URL), Header: http.Header{}}).WithContext(ctx)
}()

// Deadline reports the deadline of the request context.
func (b *Base) Deadline() (time.Time, bool) { return b.req.Context().Deadline() }

// Done reports the channel that closes when the request context ends.
func (b *Base) Done() <-chan struct{} { return b.req.Context().Done() }

// Err reports why the request context ended, or nil while it is live.
func (b *Base) Err() error { return b.req.Context().Err() }

type baseKeyType struct{}

// Value reads a string key from the store of [Base.Set] first, and falls back
// to the request context. It also answers the key that [FromContext] uses.
func (b *Base) Value(key any) any {
	switch k := key.(type) {
	case string:
		if v, ok := b.store[k]; ok {
			return v
		}
	case baseKeyType:
		return b
	}
	return b.req.Context().Value(key)
}

// FromContext recovers the Base from a context that a handler passed on, such
// as the context a [Component] renders with. ok is false when ctx carries no
// Base.
func FromContext(ctx context.Context) (*Base, bool) {
	b, ok := ctx.Value(baseKeyType{}).(*Base)
	return b, ok
}

// Set stores a value under key for the rest of the request. A middleware uses
// it to pass a value to a later handler without a new context type.
func (b *Base) Set(key string, val any) {
	if b.store == nil {
		b.store = make(map[string]any, 4)
	}
	b.store[key] = val
	b.needsCleanup = true
}

// Get reads back a value that [Base.Set] stored. ok is false when key is
// absent.
func (b *Base) Get(key string) (any, bool) {
	v, ok := b.store[key]
	return v, ok
}

// RoutePattern reports the pattern that matched, such as "/users/{id}". A 405
// and an automatic OPTIONS answer report the pattern that the path matched. A
// 404 under a scope with a prefix reports that prefix, such as "/t/{tid}",
// whose parameters [Base.Param] then reads. Anything else that matched no
// route reports "".
func (b *Base) RoutePattern() string {
	if b.route == nil {
		return ""
	}
	return b.route.pattern
}

// RouteMeta reports the values that [Router.Meta] attached to the matched
// route, outermost scope first, or nil when no route matched (a 404, a 405, an
// automatic OPTIONS answer, Pre middleware before next) or the route carries
// none. Every request of the route shares the slice, so the caller must not
// change it.
func (b *Base) RouteMeta() []any {
	if b.route == nil {
		return nil
	}
	return b.route.meta
}

// MetaAs reports the value of type T nearest to the matched route: an inner
// scope wins over an outer one, and a later Meta call over an earlier one. ok
// is false when the route carries no such value or no route matched. To
// enforce every value of a type, range over [Base.RouteMeta]. It is a function
// rather than a method so that code holding only a Context, such as the Skip
// callback of a middleware, can call it.
func MetaAs[T any](c Context) (T, bool) {
	for _, v := range slices.Backward(c.base().RouteMeta()) {
		if t, ok := v.(T); ok {
			return t, true
		}
	}
	var zero T
	return zero, false
}

// RouteHost reports the host pattern that matched, such as
// "{tenant}.example.com", or "" when the route is not scoped to a host.
func (b *Base) RouteHost() string { return b.hostPattern }

// Host reports the host of the request, lowercased and without its port.
func (b *Base) Host() string {
	if !b.hostKnown {
		b.host, b.hostKnown = normalizeHost(b.req.Host), true
		b.needsCleanup = b.needsCleanup || b.host != ""
	}
	return b.host
}

// Scheme reports "https" or "http". See [SchemeOf].
func (b *Base) Scheme() string { return SchemeOf(b.req) }

// SchemeOf reports "https" when the connection is TLS or when
// X-Forwarded-Proto names https, and "http" otherwise.
//
// The header counts whoever sent it. The RealIP middleware in front by default
// keeps only a trusted proxy's header, reduced to one value.
func SchemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	proto, _, _ := strings.Cut(r.Header.Get(HeaderXForwardedProto), ",")
	if strings.EqualFold(strings.TrimSpace(proto), "https") {
		return "https"
	}
	return "http"
}

// UserAgent reports the User-Agent header.
func (b *Base) UserAgent() string { return b.req.UserAgent() }

// Referer reports the Referer header.
func (b *Base) Referer() string { return b.req.Referer() }

// Accepts picks the best of offers for this request, or "" when none is
// acceptable. A client may send Accept more than once; every line counts.
func (b *Base) Accepts(offers ...string) string {
	return negotiate(joinAccept(b.req), offers)
}

// joinAccept folds repeated Accept lines into the one list they stand for.
func joinAccept(r *http.Request) string {
	values := r.Header.Values(HeaderAccept)
	if len(values) < 2 {
		return r.Header.Get(HeaderAccept)
	}
	return strings.Join(values, ",")
}

// Param reports the route parameter name, of the path or the host, or "" when
// the route carries no such parameter. [Base.ParamNames] lists the names the
// route has.
func (b *Base) Param(name string) string {
	v, _ := b.param(name)
	return v
}

func (b *Base) param(name string) (string, bool) {
	for i, n := range b.paramNames {
		if n == name && i < len(b.paramVals) {
			return b.paramVals[i], true
		}
	}
	return "", false
}

// ParamNames reports the parameter names of the matched route, in the order
// the pattern declares them. The caller owns the slice.
func (b *Base) ParamNames() []string { return slices.Clone(b.paramNames) }

// Method reports the HTTP method of the request.
func (b *Base) Method() string { return b.req.Method }

// Path reports the path of the request URL.
func (b *Base) Path() string { return b.req.URL.Path }

// URL reports the URL of the request.
func (b *Base) URL() *url.URL { return b.req.URL }

// SetHeader sets a header of the response, replacing any earlier value.
func (b *Base) SetHeader(key, value string) { b.res.Header().Set(key, value) }

// Vary adds names to the Vary header of the response. See [AddVary].
func (b *Base) Vary(names ...string) { AddVary(b.res.Header(), names...) }

// AddVary adds each name to the Vary header of h, once. A name already listed
// is left alone, and an h that already varies on "*" is left unchanged.
func AddVary(h http.Header, names ...string) {
	if headerContainsToken(h, HeaderVary, "*") {
		return
	}
	for _, name := range names {
		if name == "" || headerContainsToken(h, HeaderVary, name) {
			continue
		}
		h.Add(HeaderVary, name)
	}
}

func (b *Base) queryValues() url.Values {
	if b.queryCache == nil {
		b.queryCache = b.req.URL.Query()
		b.needsCleanup = true
	}
	return b.queryCache
}

// Query reports the first query parameter name, or "" when it is absent.
// [Base.QueryAs] reads it as another type, and [Base.QueryValues] tells an
// empty value from an absent one.
func (b *Base) Query(name string) string { return b.queryValues().Get(name) }

// QueryValues reports the parsed query. The router parses it once per request
// and hands back the same map, so the caller must not change it.
func (b *Base) QueryValues() url.Values { return b.queryValues() }

// IsWebSocket reports whether the request asks to upgrade to a WebSocket.
func (b *Base) IsWebSocket() bool {
	return headerContainsToken(b.req.Header, "Connection", "upgrade") &&
		headerContainsToken(b.req.Header, "Upgrade", "websocket")
}
