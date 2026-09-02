package router

import (
	"net/http"
	"slices"
	"sync/atomic"
)

type HandlerFunc[C Context] func(c C) error

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

func WrapHandlerFunc[C Context](h http.HandlerFunc) HandlerFunc[C] {
	if h == nil {
		panic("router: WrapHandlerFunc needs a handler")
	}
	return WrapHandler[C](h)
}

// WrapMiddleware adapts net/http middleware. The wrapper must call next on the
// goroutine it was given and must not return before next does: the
// request-scoped state lives in the context, which on a pooled router belongs
// to the next request as soon as this one ends.
//
// http.TimeoutHandler breaks that rule, running the handler on a new goroutine
// and returning at the deadline. Put it outside the router, or use
// middleware.Timeout, which cancels the context instead. A context whose next
// has not returned is kept out of the pool, which limits the damage but does
// not make the pattern safe.
func WrapMiddleware[C Context](m func(http.Handler) http.Handler) Middleware[C] {
	if m == nil {
		panic("router: WrapMiddleware needs middleware")
	}
	return func(next HandlerFunc[C]) HandlerFunc[C] {
		if next == nil {
			panic("router: WrapMiddleware needs a next handler")
		}
		return func(c C) error {
			b := c.base()
			b.publishParams()
			outer := b.res
			var (
				err error
				// Written by whichever goroutine runs next, so they have to be
				// atomic. Two flags: a wrapper that never calls next is
				// ordinary, one still running it is not.
				entered, returned, finished atomic.Bool
			)

			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entered.Store(true)
				if finished.Load() {
					// The request is over; the context may belong to another.
					return
				}
				b.SetRequest(r)
				if w != http.ResponseWriter(outer) {
					b.res = &Response{ResponseWriter: w, before: outer.before}
				}
				err = next(c)
				returned.Store(true)
			})

			wrapped := m(inner)
			if wrapped == nil {
				panic("router: wrapped HTTP middleware returned a nil handler")
			}
			wrapped.ServeHTTP(outer, b.req)

			// Stored before the load that follows, while the inner closure
			// loads before it stores, so whichever runs second sees the other.
			finished.Store(true)
			if entered.Load() && !returned.Load() {
				// next is still running on another goroutine.
				b.retained = true
			}
			if b.res != outer {
				if !outer.Committed {
					outer.Status = b.res.Status
					outer.Size = b.res.Size
					outer.Committed = b.res.Committed
				}
				// Hooks registered against the inner Response belong to the
				// response as a whole, and a failure is written after this.
				outer.before = b.res.before
				b.res = outer
			}
			return err
		}
	}
}

func (b *Base) publishParams() {
	for i, name := range b.paramNames {
		if i >= len(b.paramVals) {
			return
		}
		b.req.SetPathValue(name, b.paramVals[i])
	}
}
