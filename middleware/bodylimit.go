package middleware

import (
	"cmp"

	"github.com/dmitrymomot/go-router"
)

// BodyLimitConfig configures [BodyLimitWithConfig]. A Limit of zero takes
// [router.DefaultMaxBodyBytes], which then replaces the cap of the router as
// any other Limit does.
type BodyLimitConfig[C router.Context] struct {
	Skip  func(c C) bool
	Limit int64
}

// BodyLimit caps the request body at limit bytes for the routes it covers. It
// replaces the cap of [router.Router.MaxBodyBytes] there, above or below it,
// for the Bind methods and the form readers alike, so set the default with
// MaxBodyBytes and raise it on the one upload route that needs more.
//
// A Content-Length over the limit is refused before the handler runs, and a
// body that understates its length is cut off as it is read. Both report
// [router.ErrPayloadTooLarge]. A body that is cut off also closes the
// connection after the answer. Of two BodyLimits on one route, the smaller
// wins.
//
// Whatever reads the body before BodyLimit runs, such as CSRF with FromForm or
// an outer ParseForm, reads it under the cap in force then. For a body that
// [Decompress] expands, the limit also counts the expanded bytes that Bind
// reads.
//
// See Order in the package doc for where it goes.
//
// BodyLimit panics on a negative limit, and a limit of zero takes
// [router.DefaultMaxBodyBytes].
func BodyLimit[C router.Context](limit int64) router.Middleware[C] {
	return BodyLimitWithConfig(BodyLimitConfig[C]{Limit: limit})
}

// BodyLimitWithConfig is [BodyLimit] with a configuration.
//
// BodyLimitWithConfig panics on a negative Limit.
func BodyLimitWithConfig[C router.Context](cfg BodyLimitConfig[C]) router.Middleware[C] {
	if cfg.Limit < 0 {
		panic("middleware: BodyLimitWithConfig needs a Limit of zero or more")
	}
	limit := cmp.Or(cfg.Limit, router.DefaultMaxBodyBytes)

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			if c.Request().ContentLength > limit {
				return router.ErrPayloadTooLarge.WithMessage(
					"the request body is limited to %d bytes", limit)
			}

			c.SetBodyLimit(limit)
			return tooLarge(next(c), "the request body is limited to %d bytes", limit)
		}
	}
}
