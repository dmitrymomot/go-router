package router

import (
	"net/http"
	"slices"
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

func (b *Base) publishParams() {
	for i, name := range b.paramNames {
		if i >= len(b.paramVals) {
			return
		}
		b.req.SetPathValue(name, b.paramVals[i])
	}
}
