package middleware

import (
	"net/http"
	"slices"

	"github.com/dmitrymomot/go-router"
)

var errCrossOrigin = router.ErrForbidden.WithMessage("cross-origin request")

// CrossOriginConfig configures [CrossOriginWithConfig].
//
// TrustedOrigins names the origins that may send an unsafe request from
// another site, each with a scheme and a host. A path that needs no check at
// all, such as a webhook that carries its own signature, goes through Skip.
//
// OnReject answers a refused request. It gets the context and the reason that
// [http.CrossOriginProtection] reports. Return an error to report it in place
// of the request, or nil to say that OnReject wrote the answer itself; either
// way the request goes no further. A nil OnReject answers with a 403
// "cross-origin request".
type CrossOriginConfig[C router.Context] struct {
	Skip           func(c C) bool
	TrustedOrigins []string
	OnReject       func(c C, err error) error
}

// CrossOrigin refuses an unsafe request that a browser sends from another
// origin. It needs no token and no cookie: it reads Sec-Fetch-Site, and falls
// back to comparing the host of Origin with the Host of the request. It is
// [http.CrossOriginProtection] in the shape of a middleware.
//
// GET, HEAD and OPTIONS pass. A request that carries neither header is not
// from a browser, or is from one that predates both, and passes too.
//
// Compare it with [CSRF]. CrossOrigin has nothing to render into a form, sets
// no cookie, adds no Vary, and costs a request no more than two header reads,
// so a page stays cacheable and a JSON API needs nothing from the client. It
// leans wholly on the browser: a browser from before 2023 that sends neither
// header is let through. [CSRF] holds a request to a secret the attacker's
// page cannot read, so it protects that browser as well, and it takes a token
// in every form. Choose CrossOrigin for a modern audience or an API, and
// [CSRF] when old browsers matter or when a form must prove its own origin;
// do not stack the two, since CSRF alone already refuses a cross-site request.
//
// See Order in the package doc for where it goes.
func CrossOrigin[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return CrossOriginWithConfig(CrossOriginConfig[C]{})(next)
}

// CrossOriginWithConfig is [CrossOrigin] with a configuration.
//
// CrossOriginWithConfig panics on a trusted origin it cannot parse.
func CrossOriginWithConfig[C router.Context](cfg CrossOriginConfig[C]) router.Middleware[C] {
	trusted, _ := checkOrigins("CrossOriginConfig.TrustedOrigins", cfg.TrustedOrigins, false, "")
	protection := http.NewCrossOriginProtection()
	for _, o := range slices.Compact(slices.Clone(trusted)) {
		if err := protection.AddTrustedOrigin(o); err != nil {
			panic("middleware: CrossOriginConfig.TrustedOrigins got " + o + ": " + err.Error())
		}
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}
			err := protection.Check(c.Request())
			if err == nil {
				return next(c)
			}
			if cfg.OnReject == nil {
				return errCrossOrigin
			}
			return cfg.OnReject(c, err)
		}
	}
}
