package middleware

import (
	"net/http"

	"github.com/dmitrymomot/go-router"
)

// RecoverConfig configures [RecoverWithConfig].
type RecoverConfig struct {
	// Skip passes a request straight to the next handler when it returns true.
	Skip func(c router.Context) bool
}

// Recover is [RecoverWithConfig] with its default config. It is a middleware
// itself, so it goes into Use without a call:
//
//	r.Use(middleware.Recover[Ctx])
func Recover[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return RecoverWithConfig[C](RecoverConfig{})(next)
}

// RecoverWithConfig turns a panic in a handler into a 500 error, with the
// stack in the internal cause so that the error handler logs it and the client
// never sees it.
//
// The router already catches a panic that escapes the whole chain, so this is
// not what keeps the server alive. Use it to catch the panic *inside* the
// chain, where the middleware around it still runs: a logger above it then
// records the failed request, and a middleware below it still gets its
// deferred work done.
//
// It re-panics on [http.ErrAbortHandler], which is how a handler tells the
// server to drop the connection.
func RecoverWithConfig[C router.Context](cfg RecoverConfig) router.Middleware[C] {
	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) (err error) {
			if skipped(cfg.Skip, c) {
				return next(c)
			}
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
}
