package middleware

import "github.com/dmitrymomot/go-router"

// noCacheRequestHeaders are the headers of a conditional request. NoCache
// drops them, so a handler never answers 304 from an earlier copy.
var noCacheRequestHeaders = []string{
	router.HeaderIfNoneMatch,
	"If-Modified-Since",
	"If-Match",
	"If-Unmodified-Since",
	"If-Range",
	"ETag",
}

// NoCacheConfig configures [NoCacheWithConfig].
type NoCacheConfig[C router.Context] struct {
	Skip func(c C) bool
}

// NoCache tells every cache, the browser included, not to store the response
// and to ask again. It sets Cache-Control, Pragma and Expires on the response,
// and removes the conditional headers from the request, so the handler builds
// a full answer each time.
//
// See Order in the package doc for where it goes.
func NoCache[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return NoCacheWithConfig(NoCacheConfig[C]{})(next)
}

// NoCacheWithConfig is [NoCache] with a configuration.
func NoCacheWithConfig[C router.Context](cfg NoCacheConfig[C]) router.Middleware[C] {
	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}
			h := c.Request().Header
			for _, name := range noCacheRequestHeaders {
				h.Del(name)
			}
			res := c.Response().Header()
			res.Set(router.HeaderCacheControl, "no-cache, no-store, no-transform, must-revalidate, private, max-age=0")
			res.Set("Pragma", "no-cache")
			res.Set("Expires", "0")
			return next(c)
		}
	}
}
