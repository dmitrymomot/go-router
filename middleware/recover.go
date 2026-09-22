package middleware

import (
	"net/http"

	"github.com/dmitrymomot/go-router"
)

// RecoverConfig configures [RecoverWithConfig]. StackSize caps the stack that
// the error carries: zero takes [router.DefaultStackSize], and a negative size
// records the panic value alone, for a server that must not hold a stack in
// memory.
type RecoverConfig[C router.Context] struct {
	Skip      func(c C) bool
	StackSize int
}

// Recover turns a panic in a later handler into an
// [router.ErrInternalServerError] that carries a [router.PanicValue], so the
// error handler answers and the server stays up. Put it right inside
// [Logger], outside every middleware that can panic; see Order in the package
// doc.
//
// It passes [http.ErrAbortHandler] on, which is how net/http is told to drop
// the connection without a log line.
func Recover[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return RecoverWithConfig(RecoverConfig[C]{})(next)
}

// RecoverWithConfig is [Recover] with a configuration.
func RecoverWithConfig[C router.Context](cfg RecoverConfig[C]) router.Middleware[C] {
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
				err = router.PanicError(rec, cfg.StackSize)
			}()
			return next(c)
		}
	}
}
