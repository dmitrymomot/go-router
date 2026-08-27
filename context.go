// Package router implements an HTTP router whose handlers receive an
// application-defined request context.
//
// A handler has the signature func(C) error, where C is any type that embeds
// [Base]. The application therefore carries its own dependencies and helper
// methods on the context and never performs a type assertion:
//
//	type Context struct {
//		router.Base
//		DB   *sql.DB
//		User *User
//	}
//
//	r := router.New(func(w http.ResponseWriter, req *http.Request) *app.Context {
//		return &app.Context{DB: db}
//	})
//	r.GET("/users/{id}", func(c *app.Context) error {
//		return c.JSON(http.StatusOK, c.User)
//	})
//
// The router fills the embedded [Base] with the request, the response writer
// and the route parameters before it calls the handler. The factory only
// supplies the application fields.
package router

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/url"
	"time"
)

// Context is the constraint that every request context type satisfies.
//
// Embed [Base] to satisfy it. The interface holds an unexported method, so a
// type outside this package cannot implement it any other way. That guarantee
// lets the router store route parameters on any context type.
type Context interface {
	context.Context

	// Request returns the request that is in flight.
	Request() *http.Request

	// SetRequest replaces the request, which middleware does to attach a new
	// context or to rewrite a field.
	SetRequest(r *http.Request)

	// Response returns the response writer wrapper.
	Response() *Response

	// Set stores a value for the lifetime of the request.
	Set(key string, value any)

	// Get returns a value that Set stored.
	Get(key string) (any, bool)

	// Param returns the value of a route parameter.
	Param(name string) string

	// RoutePattern returns the pattern of the matched route.
	RoutePattern() string

	// Host returns the request host, without its port and in lower case.
	Host() string

	// RouteHost returns the host pattern of the matched route.
	RouteHost() string

	base() *Base
}

// maxInlineParams is the number of route parameters that a request holds
// without a heap allocation.
const maxInlineParams = 8

// Base holds the per-request state that the router owns. Embed it in the
// application context type, either by value or through a pointer.
//
// betteralign:check
//
// The router allocates one of these per request, so its layout is worth
// keeping tight.
type Base struct {
	req *http.Request
	res *Response

	store    map[string]any
	paramArr [maxInlineParams]string

	pattern string

	// host is the request host that the router matched against, without its
	// port and in lower case. Host fills it on demand.
	host string

	// hostPattern is the host pattern of the matched route, empty for a route
	// that answers every host.
	hostPattern string

	// rawTail is the part of the path that a catch-all matched, in the same
	// form as the matched path. MountHandler needs it to rebuild the path for
	// the mounted handler.
	rawTail string

	paramNames []string
	paramVals  []string

	// jsonOpts are the encoding/json/v2 options of the router.
	jsonOpts []json.Options

	// hxErr is the first failure of the htmx response chain. It lives here
	// rather than on the [HXResponse] value, so that a handler which drops one
	// link of the chain still reports the failure.
	hxErr error

	// resStorage backs res, so that a context needs one allocation and not two.
	resStorage Response

	// maxBody is the request body limit that the router applies to Bind.
	maxBody int64

	// hostIdx is the index of the matched host entry inside the router, or -1.
	// The error handler and the fallbacks read it to find the host again after
	// routing returned.
	hostIdx int32

	// hostKnown reports that host holds the answer of normalizeHost. An empty
	// host is a legitimate answer, so it cannot mark the field as unset.
	hostKnown bool

	// pathEscaped reports whether the matched path was still percent encoded.
	pathEscaped bool
}

// NewBase returns a Base that is bound to w and r. Use it when the application
// context embeds *Base instead of Base.
func NewBase(w http.ResponseWriter, r *http.Request) *Base {
	b := new(Base)
	b.init(w, r)
	return b
}

// init binds the request state. The router calls it after the context factory
// returns, so a factory never has to call it.
func (b *Base) init(w http.ResponseWriter, r *http.Request) {
	b.req = r
	if res, ok := w.(*Response); ok {
		b.res = res
	} else {
		b.resStorage = Response{ResponseWriter: w}
		b.res = &b.resStorage
	}
	b.pattern = ""
	b.paramNames = nil
	b.paramVals = b.paramArr[:0]
	b.rawTail = ""
	b.host = ""
	b.hostKnown = false
	b.hostPattern = ""
	b.hostIdx = -1
	b.pathEscaped = false
	b.hxErr = nil
	clear(b.store)
}

// setRoute records the matched route on the context.
func (b *Base) setRoute(pattern string, names, vals []string) {
	b.pattern = pattern
	b.paramNames = names
	b.paramVals = vals
}

func (b *Base) base() *Base { return b }

// Request returns the request that is in flight.
func (b *Base) Request() *http.Request { return b.req }

// SetRequest replaces the request. Middleware uses it to attach a new
// [context.Context] to the request.
func (b *Base) SetRequest(r *http.Request) { b.req = r }

// Response returns the response writer wrapper.
func (b *Base) Response() *Response { return b.res }

// ResponseWriter returns the response writer wrapper as an
// [http.ResponseWriter].
func (b *Base) ResponseWriter() http.ResponseWriter { return b.res }

// Deadline implements [context.Context].
func (b *Base) Deadline() (time.Time, bool) { return b.req.Context().Deadline() }

// Done implements [context.Context].
func (b *Base) Done() <-chan struct{} { return b.req.Context().Done() }

// Err implements [context.Context].
func (b *Base) Err() error { return b.req.Context().Err() }

// baseKeyType is the key under which [FromContext] finds the [Base].
type baseKeyType struct{}

// Value implements [context.Context]. It reads the values that [Base.Set]
// stored first, then falls back to the request context.
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

// FromContext returns the [Base] that [Base.Render] handed to a component, and
// reports whether the context carries one.
//
// A template reads the request through it, which a nav bar needs to mark the
// active link:
//
//	templ Nav() {
//		if c, ok := router.FromContext(ctx); ok {
//			<a href="/docs" aria-current={ current(c.Path() == "/docs") }>Docs</a>
//		}
//	}
//
// It answers through [context.Context.Value], so it still finds the Base after
// a template engine wraps the context, as the a-h/templ runtime does.
//
// The Base is only valid until the call that handed it over returns, because
// [NewPooled] reuses it for the next request. Read what you need and copy it;
// never store the Base itself, and never pass it to a goroutine that outlives
// the request.
//
// It returns the request state, not the application context. A template that
// needs a user or a database takes it as a parameter, which is what keeps the
// value typed.
func FromContext(ctx context.Context) (*Base, bool) {
	b, ok := ctx.Value(baseKeyType{}).(*Base)
	return b, ok
}

// Set stores a value for the lifetime of the request.
func (b *Base) Set(key string, val any) {
	if b.store == nil {
		b.store = make(map[string]any, 4)
	}
	b.store[key] = val
}

// Get returns a value that [Base.Set] stored.
func (b *Base) Get(key string) (any, bool) {
	v, ok := b.store[key]
	return v, ok
}

// RoutePattern returns the pattern of the matched route, for example
// "/users/{id}". It is empty when no route matched.
func (b *Base) RoutePattern() string { return b.pattern }

// RouteHost returns the host pattern of the matched route, for example
// "{tenant}.example.com". It is empty when the route answers every host and
// when no route matched.
func (b *Base) RouteHost() string { return b.hostPattern }

// Host returns the host that the request names, without its port, without a
// trailing dot and in lower case. Read it to resolve a tenant that reaches the
// service on a domain of its own:
//
//	tenant, err := tenants.ByDomain(c, c.Host())
//
// It reads [http.Request.Host], which carries the Host header of an HTTP/1.1
// request and the :authority of an HTTP/2 one. Trust it only as far as you
// trust the client, or the proxy in front of it, to send the right one.
func (b *Base) Host() string {
	if !b.hostKnown {
		b.host, b.hostKnown = normalizeHost(b.req.Host), true
	}
	return b.host
}

// Param returns the value of the named route parameter, or an empty string.
func (b *Base) Param(name string) string {
	v, _ := b.ParamOK(name)
	return v
}

// ParamOK returns the value of the named route parameter and reports whether
// the route declares it.
func (b *Base) ParamOK(name string) (string, bool) {
	for i, n := range b.paramNames {
		if n == name && i < len(b.paramVals) {
			return b.paramVals[i], true
		}
	}
	return "", false
}

// ParamNames returns the parameter names of the matched route, in the order in
// which the pattern declares them.
func (b *Base) ParamNames() []string { return b.paramNames }

// Method returns the request method.
func (b *Base) Method() string { return b.req.Method }

// Path returns the request path.
func (b *Base) Path() string { return b.req.URL.Path }

// URL returns the request URL.
func (b *Base) URL() *url.URL { return b.req.URL }

// Header returns the request headers.
func (b *Base) Header() http.Header { return b.req.Header }

// SetHeader sets a response header.
func (b *Base) SetHeader(key, value string) { b.res.Header().Set(key, value) }

// Vary adds header names to the Vary response header, and skips a name that
// the header already carries.
//
// A route whose answer depends on a request header has to name that header, or
// a shared cache serves one client the answer of another. A handler that
// branches on [Base.IsHTMX] is the common case:
//
//	c.Vary(router.HeaderHXRequest)
//
// [HTMXPartial] calls it for you. [AddVary] is the same for a response that
// the caller holds as an [http.Header], which is what middleware outside this
// package has.
func (b *Base) Vary(names ...string) { AddVary(b.res.Header(), names...) }

// AddVary adds header names to the Vary header of h, and skips a name that it
// already carries. [Base.Vary] is this for the response in flight.
func AddVary(h http.Header, names ...string) {
	for _, name := range names {
		if name == "" || headerContainsToken(h, HeaderVary, name) {
			continue
		}
		h.Add(HeaderVary, name)
	}
}

// Query returns the first value of the named query parameter.
func (b *Base) Query(name string) string { return b.req.URL.Query().Get(name) }

// QueryDefault returns the first value of the named query parameter, or def
// when the query does not hold it.
func (b *Base) QueryDefault(name, def string) string {
	if v := b.req.URL.Query()[name]; len(v) > 0 && v[0] != "" {
		return v[0]
	}
	return def
}

// QueryValues returns every query parameter.
func (b *Base) QueryValues() url.Values { return b.req.URL.Query() }

// Cookie returns the named cookie.
func (b *Base) Cookie(name string) (*http.Cookie, error) { return b.req.Cookie(name) }

// SetCookie adds a Set-Cookie header to the response.
func (b *Base) SetCookie(c *http.Cookie) { http.SetCookie(b.res, c) }

// IsWebSocket reports whether the request asks for a protocol upgrade to
// WebSocket.
func (b *Base) IsWebSocket() bool {
	return headerContainsToken(b.req.Header, "Connection", "upgrade") &&
		headerContainsToken(b.req.Header, "Upgrade", "websocket")
}
