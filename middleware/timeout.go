package middleware

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/dmitrymomot/go-router"
)

// DefaultTimeout is the deadline that [Timeout] applies.
const DefaultTimeout = 30 * time.Second

// TimeoutConfig configures [TimeoutWithConfig]. Duration is the deadline and
// is required. Status and Message shape the answer, and they default to 503
// with its standard text. OnTimeout answers the request itself, in place of
// that error.
type TimeoutConfig struct {
	Skip      func(c router.Context) bool
	Duration  time.Duration
	Status    int
	Message   string
	OnTimeout func(c router.Context, err error) error
}

// Timeout gives the handler a context that ends after [DefaultTimeout] and
// answers 503 when the handler runs out of time.
//
// It cancels the context; it does not stop the goroutine. A handler that
// ignores its context runs to the end, and the answer is already out.
//
// A handler that already wrote its header keeps that answer, because there is
// nothing left to replace.
func Timeout[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return TimeoutWithConfig[C](TimeoutConfig{Duration: DefaultTimeout})(next)
}

// TimeoutWithConfig is [Timeout] with a configuration.
//
// TimeoutWithConfig panics on a Duration of zero or less.
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
