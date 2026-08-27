package middleware

import (
	"net/http"

	"github.com/dmitrymomot/go-router"
)

// Recover turns a panic in a handler into a 500 error, with the stack in the
// internal cause so that the error handler logs it and the client never sees
// it.
//
// The router already catches a panic that escapes the whole chain, so Recover
// is not what keeps the server alive. Use it to catch the panic *inside* the
// chain, where the middleware around it still runs: a logger above Recover
// then records the failed request, and a middleware below it still gets its
// deferred work done.
//
// It re-panics on [http.ErrAbortHandler], which is how a handler tells the
// server to drop the connection.
func Recover[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return func(c C) (err error) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			err = router.PanicError(rec)
		}()
		return next(c)
	}
}
