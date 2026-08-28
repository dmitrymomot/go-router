package middleware

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/dmitrymomot/go-router"
)

const DefaultTimeout = 30 * time.Second

type TimeoutConfig struct {
	Skip      func(c router.Context) bool
	Duration  time.Duration
	Status    int
	Message   string
	OnTimeout func(c router.Context, err error) error
}

func Timeout[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return TimeoutWithConfig[C](TimeoutConfig{Duration: DefaultTimeout})(next)
}

func TimeoutWithConfig[C router.Context](cfg TimeoutConfig) router.Middleware[C] {
	if cfg.Duration <= 0 {
		panic("middleware: TimeoutWithConfig needs a Duration above zero")
	}
	if cfg.Status == 0 {
		cfg.Status = http.StatusServiceUnavailable
	}
	if cfg.Message == "" {
		cfg.Message = http.StatusText(cfg.Status)
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			req := c.Request()
			ctx, cancel := context.WithTimeout(req.Context(), cfg.Duration)
			defer cancel()

			timed := req.WithContext(ctx)
			c.SetRequest(timed)
			defer func() {
				if cur := c.Request(); cur != timed {
					c.SetRequest(cur.WithContext(req.Context()))
					return
				}
				c.SetRequest(req)
			}()

			err := next(c)

			if errors.Is(ctx.Err(), context.DeadlineExceeded) && !c.Response().Committed {
				cause := errors.Join(err, context.DeadlineExceeded)
				if cfg.OnTimeout != nil {
					return cfg.OnTimeout(c, cause)
				}
				return router.NewHTTPError(cfg.Status, cfg.Message).WithError(cause)
			}
			return err
		}
	}
}
