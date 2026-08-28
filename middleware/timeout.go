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

	// Duration is the deadline for one request. It has no default here:
	// [TimeoutWithConfig] panics on a duration of zero or less, because a
	// config that a file or a flag fills is how a server ends up with no
	// deadline at all and nothing that says so.
	Duration time.Duration

	// Status is the status code of the answer after a deadline. It defaults to
	// 503 Service Unavailable. OnTimeout replaces it.
	Status int

	// Message is the text of that answer. It defaults to the standard text of
	// the status code. OnTimeout replaces it.
	Message string

	// OnTimeout answers a request that ran past the deadline. It receives the
	// error that the handler returned, joined with
	// [context.DeadlineExceeded], so errors.Is finds either of them:
	//
	//	OnTimeout: func(c router.Context, err error) error {
	//		return router.ErrGatewayTimeout.WithError(err)
	//	}
	//
	// It replaces Status and Message. The error it returns reaches the error
	// handler of the router like any other.
	OnTimeout func(c router.Context, err error) error
}

// Timeout is [TimeoutWithConfig] with its default config, which is a deadline
// of [DefaultTimeout]. It is a middleware itself, so it goes into Use without
// a call:
//
//	r.Use(middleware.Timeout[Ctx])
func Timeout[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return TimeoutWithConfig[C](TimeoutConfig{Duration: DefaultTimeout})(next)
}

// TimeoutWithConfig puts a deadline on the request context and reports a
// timeout after the handler returns.
//
// It does not abandon a running handler, because a handler that keeps writing
// after the middleware answered would race with that answer. The handler has
// to watch the context, as every call that takes a [context.Context] does. A
// handler that ignores the context still runs to the end, and the client then
// sees the answer of that handler and not a timeout.
//
// The deadline lasts as long as the chain. The request goes back on the
// context it came with as this middleware returns, because that context is
// dead from there on and the error handler of the router runs later. A
// timeout reaches OnTimeout and the error handler inside the error instead,
// joined with [context.DeadlineExceeded].
//
// It panics on a Duration of zero or less. Use [Timeout] for the default
// deadline, and Skip to leave a route out of it: a middleware that reads a
// zero as "no deadline" answers a misconfigured server with a server that
// waits forever, which is the failure it exists to prevent.
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
			// cancel kills that context as this middleware returns, and the
			// error handler of the router runs after the whole chain has. Put
			// the request back on the context that came in, so what runs then
			// reads a live one: a handler that answers a 500 of its own is a
			// server fault, and a log that finds a cancelled context there
			// reads it as a client that went away instead. This defer runs
			// before cancel, which is the order that LIFO gives it.
			defer func() {
				if cur := c.Request(); cur != timed {
					// Something below installed a request of its own. Keep it,
					// on the context that came in.
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
