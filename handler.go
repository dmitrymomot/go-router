package router

import (
	"net/http"
	"slices"
)

type HandlerFunc[C Context] func(c C) error

type Middleware[C Context] func(next HandlerFunc[C]) HandlerFunc[C]

func chain[C Context](h HandlerFunc[C], mws []Middleware[C]) HandlerFunc[C] {
	for _, mw := range slices.Backward(mws) {
		h = mw(h)
	}
	return h
}

func WrapHandler[C Context](h http.Handler) HandlerFunc[C] {
	return func(c C) error {
		b := c.base()
		b.publishParams()
		h.ServeHTTP(b.res, b.req)
		return nil
	}
}

func WrapHandlerFunc[C Context](h http.HandlerFunc) HandlerFunc[C] {
	return WrapHandler[C](h)
}

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
					b.res = &Response{ResponseWriter: w, before: outer.before}
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

func (b *Base) publishParams() {
	for i, name := range b.paramNames {
		if i >= len(b.paramVals) {
			return
		}
		b.req.SetPathValue(name, b.paramVals[i])
	}
}
