package middleware

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/dmitrymomot/go-router"
)

// DefaultTimeout is the deadline that [Timeout] applies.
const DefaultTimeout = 30 * time.Second

// TimeoutConfig configures [TimeoutWithConfig]. Duration is the deadline, and
// zero takes [DefaultTimeout]. Status and Message shape the answer, and they
// default to 503 with its standard text; a Status has to be a 4xx or a 5xx.
// OnTimeout answers the request itself, in place of that error.
type TimeoutConfig[C router.Context] struct {
	Skip      func(c C) bool
	OnTimeout func(c C, err error) error
	Message   string
	Duration  time.Duration
	Status    int
}

// Timeout gives the handler a context that ends after [DefaultTimeout] and
// answers 503 when the handler runs out of time.
//
// It cancels the context; it does not stop the goroutine. A handler that
// ignores its context runs to the end, and the answer is already out.
//
// A handler that already wrote its header keeps that answer, because there is
// nothing left to replace.
//
// Put it last, inside [Idempotency] and [MinDuration], so an answer that ran
// out of time is never stored and the floor still holds; see Order in the
// package doc.
func Timeout[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return TimeoutWithConfig(TimeoutConfig[C]{})(next)
}

// TimeoutWithConfig is [Timeout] with a configuration.
//
// TimeoutWithConfig panics on a negative Duration, and on a Status that is
// neither zero nor a 4xx or a 5xx.
func TimeoutWithConfig[C router.Context](cfg TimeoutConfig[C]) router.Middleware[C] {
	if cfg.Duration < 0 {
		panic("middleware: TimeoutWithConfig needs a Duration of zero or more")
	}
	if cfg.Status != 0 && (cfg.Status < 400 || cfg.Status > 599) {
		panic(fmt.Sprintf("middleware: TimeoutWithConfig got the Status %d; take a 4xx or a 5xx, "+
			"or zero for 503", cfg.Status))
	}
	cfg.Duration = cmp.Or(cfg.Duration, DefaultTimeout)
	cfg.Status = cmp.Or(cfg.Status, http.StatusServiceUnavailable)
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

			c.SetContext(ctx)
			timed := c.Request()
			defer func() {
				if c.Request() != timed {
					c.SetContext(req.Context())
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
