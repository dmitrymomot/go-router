package middleware

import (
	"context"
	"time"

	"github.com/dmitrymomot/go-router"
)

// MinDurationConfig configures [MinDurationWithConfig]. Duration is the floor,
// and it is required.
type MinDurationConfig struct {
	Skip     func(c router.Context) bool
	Duration time.Duration
}

// MinDuration makes every answer of the routes it covers take at least d,
// counted from the moment the middleware runs. A sign-in form that mails a
// link uses it so that an address with an account and one without answer in
// the same time.
//
// The hold sits in [router.Response.Before], at the moment the header goes
// out, so an answer from the error handler waits as well. A handler that
// returns without writing waits before it hands back.
//
// The hold ends at once when the request context it found ends: the client
// went away, or a [Timeout] in front of it ran out. A Timeout behind it does
// not shorten the floor.
//
// The floor only hides work that ends inside d, so pick a d above the slowest
// path. A middleware in front of it that refuses, such as [KeyAuth], [CSRF] or
// [RateLimit], answers without the hold, so wrap every branch that depends on
// the account. A held request keeps its goroutine for d: put RateLimit in
// front, and keep the WriteTimeout of the server well above d.
//
// A hijacked connection and a 1xx informational answer are not held, because
// they do not run the callbacks of Before.
//
// MinDuration panics on a d of zero or less.
func MinDuration[C router.Context](d time.Duration) router.Middleware[C] {
	return MinDurationWithConfig[C](MinDurationConfig{Duration: d})
}

// MinDurationWithConfig is [MinDuration] with a configuration.
//
// MinDurationWithConfig panics on a Duration of zero or less.
func MinDurationWithConfig[C router.Context](cfg MinDurationConfig) router.Middleware[C] {
	if cfg.Duration <= 0 {
		panic("middleware: MinDurationWithConfig needs a Duration above zero")
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			// The context found here is kept on purpose: a Timeout further in
			// swaps in a shorter one that is still in place when the header
			// goes out.
			ctx, until := c.Request().Context(), time.Now().Add(cfg.Duration)
			res := c.Response()
			res.Before(func() { holdUntil(ctx, until) })

			err := next(c)
			if !res.Committed {
				holdUntil(ctx, until)
			}
			return err
		}
	}
}

func holdUntil(ctx context.Context, until time.Time) {
	wait := time.Until(until)
	if wait <= 0 {
		return
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
