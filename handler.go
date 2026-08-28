package router

import (
	"net/http"
	"slices"
)

// HandlerFunc handles a request. It returns an error to hand control to the
// error handler of the router, which turns the error into a response.
type HandlerFunc[C Context] func(c C) error

// Middleware wraps a handler. The router applies a chain from left to right,
// so the first middleware is the outermost one.
type Middleware[C Context] func(next HandlerFunc[C]) HandlerFunc[C]

// chain wraps h with mws. mws[0] ends up outermost.
func chain[C Context](h HandlerFunc[C], mws []Middleware[C]) HandlerFunc[C] {
	for _, mw := range slices.Backward(mws) {
		h = mw(h)
	}
	return h
}

// WrapHandler adapts an [http.Handler] to a HandlerFunc. The handler writes
// through the [Response] wrapper, so the status and the byte count stay
// visible to middleware.
//
// The route parameters reach the request first, so that the wrapped handler
// reads them with [http.Request.PathValue]. [http.Request.Pattern] already
// carries the matched pattern, which the router publishes for every request.
func WrapHandler[C Context](h http.Handler) HandlerFunc[C] {
	return func(c C) error {
		b := c.base()
		b.publishParams()
		h.ServeHTTP(b.res, b.req)
		return nil
	}
}

// WrapHandlerFunc adapts an [http.HandlerFunc] to a HandlerFunc.
func WrapHandlerFunc[C Context](h http.HandlerFunc) HandlerFunc[C] {
	return WrapHandler[C](h)
}

// WrapMiddleware adapts standard middleware, of the shape
// func(http.Handler) http.Handler, to [Middleware]. Use it to reuse the
// net/http middleware ecosystem:
//
//	r.Use(router.WrapMiddleware[*app.Context](gziphandler.GzipHandler))
//
// The adapter keeps the request that the standard middleware passes down. When
// that middleware also replaces the response writer, the adapter binds the
// context to the replacement, then restores the outer [Response] and copies
// the status back if the outer writer is still uncommitted.
//
// The route parameters reach the request before the standard middleware runs,
// so that it reads them with [http.Request.PathValue], next to the pattern
// that [http.Request.Pattern] already carries. A tracing middleware therefore
// labels its span with the route rather than with the raw URL, whose
// cardinality is unbounded.
func WrapMiddleware[C Context](m func(http.Handler) http.Handler) Middleware[C] {
	return func(next HandlerFunc[C]) HandlerFunc[C] {
		return func(c C) error {
			b := c.base()
			b.publishParams()
			outer := b.res
			var err error

			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b.SetRequest(r)
				if w != http.ResponseWriter(outer) {
					b.res = &Response{ResponseWriter: w}
				}
				err = next(c)
			})

			m(inner).ServeHTTP(outer, b.req)

			if b.res != outer {
				if !outer.Committed {
					outer.Status = b.res.Status
					outer.Size = b.res.Size
					outer.Committed = b.res.Committed
				}
				b.res = outer
			}
			return err
		}
	}
}

// publishParams copies the route parameters onto the request, which is what
// [http.Request.PathValue] reads. The router keeps them on the context
// instead, because writing them here costs a map for every request, and only a
// standard handler or a standard middleware ever asks for them.
func (b *Base) publishParams() {
	for i, name := range b.paramNames {
		if i >= len(b.paramVals) {
			return
		}
		b.req.SetPathValue(name, b.paramVals[i])
	}
}
