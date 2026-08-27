package router

import "net/http"

// HandlerFunc handles a request. It returns an error to hand control to the
// error handler of the router, which turns the error into a response.
type HandlerFunc[C Context] func(c C) error

// Middleware wraps a handler. The router applies a chain from left to right,
// so the first middleware is the outermost one.
type Middleware[C Context] func(next HandlerFunc[C]) HandlerFunc[C]

// chain wraps h with mws. mws[0] ends up outermost.
func chain[C Context](h HandlerFunc[C], mws []Middleware[C]) HandlerFunc[C] {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// WrapHandler adapts an [http.Handler] to a HandlerFunc. The handler writes
// through the [Response] wrapper, so the status and the byte count stay
// visible to middleware.
func WrapHandler[C Context](h http.Handler) HandlerFunc[C] {
	return func(c C) error {
		b := c.base()
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
func WrapMiddleware[C Context](m func(http.Handler) http.Handler) Middleware[C] {
	return func(next HandlerFunc[C]) HandlerFunc[C] {
		return func(c C) error {
			b := c.base()
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
