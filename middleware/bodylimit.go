package middleware

import (
	"net/http"

	"github.com/dmitrymomot/go-router"
)

// BodyLimitConfig configures [BodyLimitWithConfig]. A Limit of zero or less
// takes [router.DefaultMaxBodyBytes].
type BodyLimitConfig struct {
	Skip  func(c router.Context) bool
	Limit int64
}

// BodyLimit caps the request body at limit bytes for the routes it covers,
// which is what a single upload route needs above the setting of the router.
//
// A Content-Length over the limit is refused before the handler runs, and a
// body that understates its length is cut off as it is read. Both report
// [router.ErrPayloadTooLarge].
func BodyLimit[C router.Context](limit int64) router.Middleware[C] {
	return BodyLimitWithConfig[C](BodyLimitConfig{Limit: limit})
}

// BodyLimitWithConfig is [BodyLimit] with a configuration.
func BodyLimitWithConfig[C router.Context](cfg BodyLimitConfig) router.Middleware[C] {
	limit := cfg.Limit
	if limit <= 0 {
		limit = router.DefaultMaxBodyBytes
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			req := c.Request()
			if req.ContentLength > limit {
				return router.ErrPayloadTooLarge.WithMessage(
					"the request body is limited to %d bytes", limit)
			}

			if req.Body != nil && req.Body != http.NoBody {
				capped := *req
				capped.Body = http.MaxBytesReader(c.Response(), req.Body, limit)
				c.SetRequest(&capped)
			}

			return tooLarge(next(c), "the request body is limited to %d bytes", limit)
		}
	}
}
