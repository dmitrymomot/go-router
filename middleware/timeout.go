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

// TimeoutConfig configures [TimeoutWithConfig].
type TimeoutConfig struct {
	// Skip passes a request straight to the next handler when it returns true.
	Skip func(c router.Context) bool

	// Duration is the deadline for one request. Zero disables the middleware.
	Duration time.Duration

	// Status is the status code of the answer after a deadline. It defaults to
	// 503 Service Unavailable.
	Status int

	// Message is the text of that answer. It defaults to the standard text of
	// the status code.
	Message string
}

// Timeout returns the middleware with its default config, which is a deadline
// of [DefaultTimeout].
func Timeout[C router.Context]() router.Middleware[C] {
	return TimeoutWithConfig[C](TimeoutConfig{Duration: DefaultTimeout})
}

// TimeoutWithConfig puts a deadline on the request context and reports a
// timeout after the handler returns.
//
// It does not abandon a running handler, because a handler that keeps writing
// after the middleware answered would race with that answer. The handler has
// to watch the context, as every call that takes a [context.Context] does. A
// handler that ignores the context still runs to the end, and the client then
// sees the answer of that handler and not a timeout.
func TimeoutWithConfig[C router.Context](cfg TimeoutConfig) router.Middleware[C] {
	if cfg.Status == 0 {
		cfg.Status = http.StatusServiceUnavailable
	}
	if cfg.Message == "" {
		cfg.Message = http.StatusText(cfg.Status)
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if cfg.Duration <= 0 || skipped(cfg.Skip, c) {
				return next(c)
			}

			req := c.Request()
			ctx, cancel := context.WithTimeout(req.Context(), cfg.Duration)
			defer cancel()

			c.SetRequest(req.WithContext(ctx))
			err := next(c)

			if errors.Is(ctx.Err(), context.DeadlineExceeded) && !c.Response().Committed {
				return router.NewHTTPError(cfg.Status, cfg.Message).WithError(
					errors.Join(err, context.DeadlineExceeded))
			}
			return err
		}
	}
}
