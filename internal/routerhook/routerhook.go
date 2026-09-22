// Package routerhook lets package routertest reach the state of a router.Base
// that the public API of package router does not expose. Package router fills
// the hooks when it initializes, and only packages of this module can import
// this one.
//
// The hooks take any, because this package cannot import router: router
// imports it. Package router checks the types.
package routerhook

import "net/http"

var (
	// SetRoute gives b, a *router.Base, a route pattern and its parameters.
	// names and vals pair up by index.
	SetRoute func(b any, pattern string, names, vals []string)

	// SetCookieCodec gives b, a *router.Base, the codec cc, a
	// *router.CookieCodec, as Router.CookieCodec gives it to the contexts of a
	// router. The caller checks cc with CheckCookieCodec first.
	SetCookieCodec func(b, cc any)

	// CheckCookieCodec panics on a nil codec and on one that NewCookieCodec
	// did not build. caller starts the message, as in "routertest: FlashCookie".
	CheckCookieCodec func(cc any, caller string)

	// CookieCodec reports the *router.CookieCodec of h when h is a router, or
	// nil when it is not or has none.
	CookieCodec func(h http.Handler) any
)
