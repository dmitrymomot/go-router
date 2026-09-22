package router

import (
	"net/http"

	"github.com/dmitrymomot/go-router/internal/routerhook"
)

// The hooks give package routertest what a context built outside a router
// lacks, without a public API that only a test would call.
func init() {
	routerhook.SetRoute = func(b any, pattern string, names, vals []string) {
		b.(*Base).setTestRoute(pattern, names, vals)
	}
	routerhook.SetCookieCodec = func(b, cc any) {
		b.(*Base).setCodec(cc.(*CookieCodec))
	}
	routerhook.CheckCookieCodec = func(cc any, caller string) {
		codec, _ := cc.(*CookieCodec)
		mustBeBuiltCodec(codec, caller)
	}
	routerhook.CookieCodec = func(h http.Handler) any {
		if cc := cookieCodecOf(h); cc != nil {
			return cc
		}
		return nil
	}
}

// setTestRoute gives b a route pattern and its parameters, as routing a
// request would. names and vals pair up by index. An empty pattern leaves b
// with no route.
func (b *Base) setTestRoute(pattern string, names, vals []string) {
	var rec *routeRecord
	if pattern != "" {
		rec = &routeRecord{pattern: pattern}
	}
	b.needsCleanup = true
	b.setRoute(rec, names, vals)
}

// setCodec gives b the codec that Router.CookieCodec gives the contexts of a
// router. It copies the settings of b, so no other Base changes.
func (b *Base) setCodec(cc *CookieCodec) {
	o := *b.opts()
	o.codec = cc
	b.ropts = &o
}

// mustBeBuiltCodec panics on a nil codec and on one that NewCookieCodec did
// not build, which has no key to sign with. caller starts the message.
func mustBeBuiltCodec(cc *CookieCodec, caller string) {
	if cc == nil {
		panic(caller + " needs a codec")
	}
	if len(cc.keys) == 0 {
		panic(caller + " needs a codec built by NewCookieCodec")
	}
}

// cookieCodecOf reports the codec that h signs cookies with when h is a
// Router, or nil when it is not or has none.
func cookieCodecOf(h http.Handler) *CookieCodec {
	if r, ok := h.(interface{ cookieCodec() *CookieCodec }); ok {
		return r.cookieCodec()
	}
	return nil
}
