package middleware

import (
	"fmt"
	"net/http"
	"runtime/debug"

	"github.com/dmitrymomot/go-router"
)

// Recover turns a panic in a handler into a 500 error. It keeps the stack in
// the internal cause of the error, so the error handler logs it and the client
// never sees it.
//
// It re-panics on [http.ErrAbortHandler], which is the way a handler tells the
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
			cause, ok := rec.(error)
			if !ok {
				cause = fmt.Errorf("%v", rec)
			}
			err = router.ErrInternalServerError.WithError(
				fmt.Errorf("panic: %w\n\n%s", cause, debug.Stack()))
		}()
		return next(c)
	}
}
