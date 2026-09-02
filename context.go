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
	Response() *Response
	Set(key string, value any)
	Get(key string) (any, bool)
	Param(name string) string
	RoutePattern() string
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
	pattern     string
	host        string
	hostPattern string
	rawTail     string
	paramNames  []string
	paramVals   []string
	ropts       *routerOpts

	// One word rather than two error fields, which would push Base from 360 to
	// 384 bytes and its embedder into the next size class.
	deferred      *deferredErrors
	resStorage    Response
	hostIdx       int32
	errorScopeIdx int32
	hostKnown     bool
	pathEscaped   bool
	errorRouted   bool
	needsCleanup  bool

	// Routing matches the path trimmed of its trailing slash, so a mounted
	// handler has to be told the slash was there.
	tailSlash bool
}

type routerOpts struct {
	jsonOpts     []json.Options
	logger       *slog.Logger
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

// Every request pays for this, so it has to stay under the inline budget:
// go build -gcflags='-m=2' . 2>&1 | grep 'Base).init'
func (b *Base) init(w http.ResponseWriter, r *http.Request) {
	res, ok := w.(*Response)
	if !ok {
		b.resStorage = Response{ResponseWriter: w}
		res = &b.resStorage
	}
	b.req, b.res = r, res
	b.pattern, b.rawTail = "", ""
	b.paramNames, b.paramVals = nil, b.paramArr[:0]
	b.host, b.hostKnown, b.hostPattern = "", false, ""
	b.hostIdx = -1
	b.pathEscaped, b.errorRouted, b.tailSlash = false, false, false
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

type deferredErrors struct {
	form error
	hx   error
}

func (b *Base) deferrals() *deferredErrors {
	if b.deferred == nil {
		b.deferred = new(deferredErrors)
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

func (b *Base) hxError() error {
	if b.deferred == nil {
		return nil
	}
	return b.deferred.hx
}

func (b *Base) setHXError(err error) {
	if d := b.deferrals(); d.hx == nil {
		d.hx = err
	}
}

func (b *Base) setRoute(pattern string, names, vals []string) {
	b.pattern = pattern
	b.paramNames = names
	b.paramVals = vals
}

// SetRouteForTest gives b a route pattern and its parameters, so a test can
// call a handler that reads them without a router. names and vals pair up by
// index.
//
// SetRouteForTest panics if b is nil.
func SetRouteForTest(b *Base, pattern string, names, vals []string) {
	if b == nil {
		panic("router: SetRouteForTest needs a Base")
	}
	b.needsCleanup = true
	b.setRoute(pattern, names, vals)
}

func (b *Base) base() *Base { return b }

// Request reports the request that the handler answers.
func (b *Base) Request() *http.Request { return b.req }

// SetRequest replaces the request. A middleware calls it after it wraps the
// body or adds a value to the request context. The cached query and host are
// dropped, so the next read takes them from r.
//
// SetRequest panics if r is nil.
func (b *Base) SetRequest(r *http.Request) {
	if r == nil {
		panic("router: SetRequest needs a request")
	}
	b.req = r
	b.queryCache = nil
	b.host, b.hostKnown = "", false
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
// number of bytes written.
func (b *Base) Response() *Response { return b.res }

// ResponseWriter reports the response as an [http.ResponseWriter], for a
// library that takes one.
func (b *Base) ResponseWriter() http.ResponseWriter { return b.res }

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

// RoutePattern reports the pattern that matched, such as "/users/{id}", or ""
// when no route matched.
func (b *Base) RoutePattern() string { return b.pattern }

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

// IsTLS reports whether the client reached this server over TLS. It reads the
// connection and not a header, so a request that a proxy forwarded in plain
// HTTP reports false.
func (b *Base) IsTLS() bool { return b.req.TLS != nil }

// Scheme reports "https" or "http". See [SchemeOf].
func (b *Base) Scheme() string { return SchemeOf(b.req) }

// SchemeOf reports "https" when the connection is TLS or when
// X-Forwarded-Proto names https, and "http" otherwise.
//
// The header counts whoever sent it. Put the RealIP middleware in front to
// drop the header of a peer you do not trust.
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

// Param reports the route parameter name, or "" when the route carries no such
// parameter. Use [Base.ParamOK] to tell an empty value from a missing one.
func (b *Base) Param(name string) string {
	v, _ := b.ParamOK(name)
	return v
}

// ParamOK reports the route parameter name. ok is false when the route carries
// no such parameter.
func (b *Base) ParamOK(name string) (string, bool) {
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

// Header reports the headers that came in with the request. Write to
// [Base.SetHeader] or to the header of [Base.Response] to answer.
func (b *Base) Header() http.Header { return b.req.Header }

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
func (b *Base) Query(name string) string { return b.queryValues().Get(name) }

// QueryOK reports the first query parameter name. ok is false when the query
// carries no such parameter.
func (b *Base) QueryOK(name string) (string, bool) {
	v, ok := b.queryValues()[name]
	if !ok || len(v) == 0 {
		return "", false
	}
	return v[0], true
}

// QueryDefault reports the first query parameter name, or def when it is
// absent or empty.
func (b *Base) QueryDefault(name, def string) string {
	if v := b.queryValues()[name]; len(v) > 0 && v[0] != "" {
		return v[0]
	}
	return def
}

// QueryValues reports the parsed query. The router parses it once per request
// and hands back the same map, so the caller must not change it.
func (b *Base) QueryValues() url.Values { return b.queryValues() }

// Cookie reads a cookie of the request. It reports [http.ErrNoCookie] when the
// request carries no such cookie. See [Base.SignedCookie] for a cookie the
// client cannot forge.
func (b *Base) Cookie(name string) (*http.Cookie, error) { return b.req.Cookie(name) }

// SetCookie adds a Set-Cookie header to the response.
func (b *Base) SetCookie(c *http.Cookie) { http.SetCookie(b.res, c) }

// IsWebSocket reports whether the request asks to upgrade to a WebSocket.
func (b *Base) IsWebSocket() bool {
	return headerContainsToken(b.req.Header, "Connection", "upgrade") &&
		headerContainsToken(b.req.Header, "Upgrade", "websocket")
}
