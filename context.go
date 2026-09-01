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
// router that is the difference between zero and one. Router.Params and
// InlineParamBudget let a route table assert it stays under.
const maxInlineParams = 4

// betteralign:check
type Base struct {
	req *http.Request
	res *Response

	store      map[string]any
	paramArr   [maxInlineParams]string
	queryCache url.Values

	pattern     string
	host        string
	hostPattern string
	rawTail     string

	paramNames []string
	paramVals  []string
	ropts      *routerOpts

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
}

type routerOpts struct {
	jsonOpts     []json.Options
	logger       *slog.Logger
	maxBody      int64
	maxMultipart int64
	strictBind   bool
}

var defaultRouterOpts = &routerOpts{maxBody: DefaultMaxBodyBytes}

func (b *Base) opts() *routerOpts {
	if b.ropts == nil {
		return defaultRouterOpts
	}
	return b.ropts
}

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
	b.pathEscaped, b.errorRouted = false, false
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

func SetRouteForTest(b *Base, pattern string, names, vals []string) {
	if b == nil {
		panic("router: SetRouteForTest needs a Base")
	}
	b.needsCleanup = true
	b.setRoute(pattern, names, vals)
}

func (b *Base) base() *Base { return b }

func (b *Base) Request() *http.Request { return b.req }

func (b *Base) SetRequest(r *http.Request) {
	if r == nil {
		panic("router: SetRequest needs a request")
	}
	b.req = r
	b.queryCache = nil
	b.host, b.hostKnown = "", false
}

func (b *Base) Logger() *slog.Logger {
	if l := b.opts().logger; l != nil {
		return l
	}
	return slog.Default()
}

func (b *Base) Response() *Response { return b.res }

func (b *Base) ResponseWriter() http.ResponseWriter { return b.res }

func (b *Base) Deadline() (time.Time, bool) { return b.req.Context().Deadline() }

func (b *Base) Done() <-chan struct{} { return b.req.Context().Done() }

func (b *Base) Err() error { return b.req.Context().Err() }

type baseKeyType struct{}

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

func FromContext(ctx context.Context) (*Base, bool) {
	b, ok := ctx.Value(baseKeyType{}).(*Base)
	return b, ok
}

func (b *Base) Set(key string, val any) {
	if b.store == nil {
		b.store = make(map[string]any, 4)
	}
	b.store[key] = val
	b.needsCleanup = true
}

func (b *Base) Get(key string) (any, bool) {
	v, ok := b.store[key]
	return v, ok
}

func (b *Base) RoutePattern() string { return b.pattern }

func (b *Base) RouteHost() string { return b.hostPattern }

func (b *Base) Host() string {
	if !b.hostKnown {
		b.host, b.hostKnown = normalizeHost(b.req.Host), true
		b.needsCleanup = b.needsCleanup || b.host != ""
	}
	return b.host
}

func (b *Base) IsTLS() bool { return b.req.TLS != nil }

func (b *Base) Scheme() string { return SchemeOf(b.req) }

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

func (b *Base) UserAgent() string { return b.req.UserAgent() }

func (b *Base) Referer() string { return b.req.Referer() }

func (b *Base) Accepts(offers ...string) string {
	return negotiate(b.req.Header.Get(HeaderAccept), offers)
}

func (b *Base) Param(name string) string {
	v, _ := b.ParamOK(name)
	return v
}

func (b *Base) ParamOK(name string) (string, bool) {
	for i, n := range b.paramNames {
		if n == name && i < len(b.paramVals) {
			return b.paramVals[i], true
		}
	}
	return "", false
}

func (b *Base) ParamNames() []string { return slices.Clone(b.paramNames) }

func (b *Base) Method() string { return b.req.Method }

func (b *Base) Path() string { return b.req.URL.Path }

func (b *Base) URL() *url.URL { return b.req.URL }

func (b *Base) Header() http.Header { return b.req.Header }

func (b *Base) SetHeader(key, value string) { b.res.Header().Set(key, value) }

func (b *Base) Vary(names ...string) { AddVary(b.res.Header(), names...) }

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

func (b *Base) Query(name string) string { return b.queryValues().Get(name) }

func (b *Base) QueryOK(name string) (string, bool) {
	v, ok := b.queryValues()[name]
	if !ok || len(v) == 0 {
		return "", false
	}
	return v[0], true
}

func (b *Base) QueryDefault(name, def string) string {
	if v := b.queryValues()[name]; len(v) > 0 && v[0] != "" {
		return v[0]
	}
	return def
}

func (b *Base) QueryValues() url.Values { return b.queryValues() }

func (b *Base) Cookie(name string) (*http.Cookie, error) { return b.req.Cookie(name) }

func (b *Base) SetCookie(c *http.Cookie) { http.SetCookie(b.res, c) }

func (b *Base) IsWebSocket() bool {
	return headerContainsToken(b.req.Header, "Connection", "upgrade") &&
		headerContainsToken(b.req.Header, "Upgrade", "websocket")
}
