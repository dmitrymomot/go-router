package useragent

import "github.com/dmitrymomot/go-router"

// storeKey is where [Of] keeps the parse of the request, in the store of
// router.Base.Set. The prefix keeps it apart from the keys of the
// application.
const storeKey = "useragent.parsed"

// Of reports the user agent of the request that c answers, parsed from its
// headers with [ParseHeaders]. The first call parses, and every later call in
// the same request, from a middleware or the handler, reads that parse back.
func Of(c router.Context) UserAgent {
	if v, ok := c.Get(storeKey); ok {
		if ua, ok := v.(UserAgent); ok {
			return ua
		}
	}
	ua := ParseHeaders(c.Request().Header)
	c.Set(storeKey, ua)
	return ua
}
