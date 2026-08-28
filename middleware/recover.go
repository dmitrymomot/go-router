package middleware

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/dmitrymomot/go-router"
)

type RecoverConfig struct {
	Skip         func(c router.Context) bool
	StackSize    int
	DisableStack bool
}

func Recover[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return RecoverWithConfig[C](RecoverConfig{})(next)
}

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
