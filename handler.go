package router

import (
	"net/http"
	"slices"
)

// HandlerFunc answers one request. The router builds C, fills its embedded
// [Base], and calls the handler. A non-nil error goes to the error handler of
// the router, which writes the response.
type HandlerFunc[C Context] func(c C) error

// Middleware wraps a handler in another handler. It runs in the order that
// [Router.Use] received it, and it must return a non-nil handler.
type Middleware[C Context] func(next HandlerFunc[C]) HandlerFunc[C]

func chain[C Context](h HandlerFunc[C], mws []Middleware[C]) HandlerFunc[C] {
	for _, mw := range slices.Backward(mws) {
		if mw == nil {
			panic("router: middleware must not be nil")
		}
		if h = mw(h); h == nil {
			panic("router: middleware returned a nil handler")
		}
	}
	return h
}

// withRouteMeta publishes rec, the route with its Meta values, before the chain
// of the route runs. init resets the field on every request, so a pooled
// context needs no cleanup.
func withRouteMeta[C Context](h HandlerFunc[C], rec *routeRecord) HandlerFunc[C] {
	return func(c C) error {
		c.base().route = rec
		return h(c)
	}
}

// WrapHandler turns a standard library handler into a [HandlerFunc]. The
// wrapped handler reads the route parameters through [http.Request.PathValue],
// and the returned handler never reports an error.
//
// WrapHandler panics if h is nil.
func WrapHandler[C Context](h http.Handler) HandlerFunc[C] {
	if h == nil {
		panic("router: WrapHandler needs a handler")
	}
	return func(c C) error {
		b := c.base()
		b.publishParams()
		h.ServeHTTP(b.res, b.req)
		return nil
	}
}

// WrapHandlerFunc is [WrapHandler] for a plain function.
//
// WrapHandlerFunc panics if h is nil.
func WrapHandlerFunc[C Context](h http.HandlerFunc) HandlerFunc[C] {
	if h == nil {
		panic("router: WrapHandlerFunc needs a handler")
	}
	return WrapHandler[C](h)
}

func (b *Base) publishParams() {
	for i, name := range b.paramNames {
		if i >= len(b.paramVals) {
			return
		}
		b.req.SetPathValue(name, b.paramVals[i])
	}
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
