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

// WrapMiddleware adapts net/http middleware. The wrapped middleware must call
// next on the goroutine it was given and must not return before next returns:
// the request-scoped state lives in the context, and on a pooled router that
// context belongs to the next request as soon as this one ends.
//
// http.TimeoutHandler is the middleware that breaks the rule -- it runs the
// handler on a new goroutine and returns at the deadline. Wrapping it here
// leaves that goroutine writing into a live request. Use it outside the router,
// around the whole handler, or use middleware.Timeout, which cancels the
// context instead of abandoning the goroutine.
//
// A context whose next has not returned is not recycled, so the damage stops at
// one leaked context rather than reaching the request that follows.
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
				// Written by whichever goroutine runs next and read here, so
				// they have to be atomic: a wrapper that breaks the rule below
				// is exactly the case this is trying to detect. Two flags,
				// because a wrapper that never calls next at all -- an auth
				// check that rejects, say -- is ordinary and must not be
				// mistaken for one that is still running it.
				entered, returned, finished atomic.Bool
			)

			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entered.Store(true)
				if finished.Load() {
					// The wrapper has already returned, so this request is over
					// and the context may belong to the next one by now.
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

			// Store before the load, as the inner closure does in the opposite
			// order: whichever of the two runs second sees the other's write, so
			// a late next either finds finished set and does nothing, or is seen
			// here and keeps the context out of the pool.
			finished.Store(true)
			if entered.Load() && !returned.Load() {
				// The wrapper handed control back while next was still running,
				// so another goroutine is still writing through this context.
				b.retained = true
			}
			if b.res != outer {
				if !outer.Committed {
					outer.Status = b.res.Status
					outer.Size = b.res.Size
					outer.Committed = b.res.Committed
				}
				// Hooks registered while the inner Response was in place belong
				// to the response as a whole. Without this a middleware that
				// registers one and then fails -- KeyAuth setting
				// WWW-Authenticate before it answers 401 -- lost the header,
				// because the failure is written after this restore.
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
