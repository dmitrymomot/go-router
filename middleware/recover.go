package middleware

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/dmitrymomot/go-router"
)

// RecoverConfig configures [RecoverWithConfig]. StackSize caps the stack that
// the error carries, and zero takes [router.DefaultStackSize]. DisableStack
// records the panic value alone, for a server that must not hold a stack in
// memory.
type RecoverConfig struct {
	Skip         func(c router.Context) bool
	StackSize    int
	DisableStack bool
}

// Recover turns a panic in a later handler into an
// [router.ErrInternalServerError] that carries a [router.PanicValue], so the
// error handler answers and the server stays up. Put it outermost.
//
// It passes [http.ErrAbortHandler] on, which is how net/http is told to drop
// the connection without a log line.
func Recover[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return RecoverWithConfig[C](RecoverConfig{})(next)
}

// RecoverWithConfig is [Recover] with a configuration.
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
				if cfg.DisableStack {
					err = router.ErrInternalServerError.WithError(
						&router.PanicValue{Value: rec, Err: panicCause(rec)})
					return
				}
				err = router.PanicErrorSize(rec, cfg.StackSize)
			}()
			return next(c)
		}
	}
}

func panicCause(rec any) error {
	if err, ok := rec.(error); ok {
		return err
	}
	return errors.New(fmt.Sprint(rec))
}
