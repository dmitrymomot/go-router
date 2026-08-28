package middleware

import (
	"net/http"

	"github.com/dmitrymomot/go-router"
)

// BodyLimitConfig configures [BodyLimitWithConfig].
type BodyLimitConfig struct {
	// Skip passes a request straight to the next handler when it returns true.
	Skip func(c router.Context) bool

	// Limit is the largest request body in bytes. Zero uses
	// [router.DefaultMaxBodyBytes].
	Limit int64
}

// BodyLimit caps the request body at limit bytes. It is
// [BodyLimitWithConfig] with the limit alone:
//
//	r.Use(middleware.BodyLimit[Ctx](1 << 20))
//
// A limit of zero or less uses [router.DefaultMaxBodyBytes].
func BodyLimit[C router.Context](limit int64) router.Middleware[C] {
	return BodyLimitWithConfig[C](BodyLimitConfig{Limit: limit})
}

// BodyLimitWithConfig caps the request body.
//
// It answers a request that declares a longer Content-Length with
// [router.ErrPayloadTooLarge] before it reads a byte, then replaces the body
// with an [http.MaxBytesReader], so a client that understates the length, or
// sends none at all, stops at the same point.
//
// [router.Router.MaxBodyBytes] caps the body that Bind reads, and only there.
// This caps the body itself, which is what a handler that reads
// [http.Request.Body] or streams an upload needs. Both fit together: the
// middleware guards every reader, and Bind keeps its own cap for a router that
// runs without it.
//
// A body that runs past the limit reports an [http.MaxBytesError] to whoever
// reads it. Bind turns that into [router.ErrPayloadTooLarge] itself, and this
// middleware turns one that reaches it from a handler into the same error, so
// errors.Is finds it either way.
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
