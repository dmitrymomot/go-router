package middleware

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/dmitrymomot/go-router"
)

// TimeoutConfig configures [TimeoutConfig.Middleware].
type TimeoutConfig struct {
	// Duration is the deadline for one request. Zero disables the middleware.
	Duration time.Duration

	// Status is the status code of the answer after a deadline. It defaults to
	// 503 Service Unavailable.
	Status int

	// Message is the text of that answer. It defaults to the standard text of
	// the status code.
	Message string
}

// Timeout returns a configuration with the given deadline.
func Timeout(d time.Duration) TimeoutConfig { return TimeoutConfig{Duration: d} }

// Middleware puts a deadline on the request context and reports a timeout
// after the handler returns.
//
// It does not abandon a running handler, because a handler that keeps writing
// after the middleware answered would race with that answer. The handler has
// to watch the context, as every call that takes a [context.Context] does. A
// handler that ignores the context still runs to the end, and the client then
// sees the answer of that handler and not a timeout.
func (cfg TimeoutConfig) Middleware[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	status := cfg.Status
	if status == 0 {
		status = http.StatusServiceUnavailable
	}
	message := cfg.Message
	if message == "" {
		message = http.StatusText(status)
	}

	return func(c C) error {
		if cfg.Duration <= 0 {
			return next(c)
		}
		req := c.Request()
		ctx, cancel := context.WithTimeout(req.Context(), cfg.Duration)
		defer cancel()

		c.SetRequest(req.WithContext(ctx))
		err := next(c)

		if errors.Is(ctx.Err(), context.DeadlineExceeded) && !c.Response().Committed {
			return router.NewHTTPError(status, message).WithError(
				errors.Join(err, context.DeadlineExceeded))
		}
		return err
	}
}
