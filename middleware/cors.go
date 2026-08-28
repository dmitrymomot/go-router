package middleware

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
)

// CORSConfig configures [CORSWithConfig].
type CORSConfig struct {
	// Skip passes a request straight to the next handler when it returns true.
	Skip func(c router.Context) bool

	// AllowOrigins lists the origins that may read the response. An entry
	// names a scheme and a host, "https://app.example", and it matches that
	// one origin: the comparison is exact, so a wildcard host matches nothing.
	// The single entry "*" allows every origin, and an empty list allows none.
	//
	// [CORSWithConfig] panics on an entry that is not an origin. An entry that
	// carries a path, or that leaves the scheme out, matches no origin at all,
	// and the mistake would otherwise surface as a blocked request in
	// production and nowhere else.
	AllowOrigins []string

	// AllowOriginFunc decides per origin and takes precedence over
	// AllowOrigins. It reads the context, so a host-routed service answers for
	// the tenant that this request reached:
	//
	//	AllowOriginFunc: func(c router.Context, origin string) (bool, error) {
	//		return tenants.AllowsOrigin(c, c.Host(), origin)
	//	}
	//
	// An error it returns becomes the error of the request, and the answer
	// carries no CORS header, because a lookup that failed decided nothing.
	AllowOriginFunc func(c router.Context, origin string) (bool, error)

	// AllowMethods lists the methods of a preflight answer. An empty list
	// takes the Allow header that the router wrote for the matched path, which
	// names the methods that the route answers, and falls back to the safe and
	// the common write methods for a route that handles OPTIONS itself.
	AllowMethods []string

	// AllowHeaders lists the request headers that a preflight answer permits.
	// An empty list echoes the headers that the preflight asked for.
	AllowHeaders []string

	// ExposeHeaders lists the response headers that the browser reveals to the
	// script. A preflight answer never carries them, because it carries no
	// body for a script to read.
	ExposeHeaders []string

	// AllowCredentials permits cookies and the Authorization header, and the
	// answer then names the origin in place of "*".
	//
	// [CORSWithConfig] panics when it combines with the origin "*". Every
	// origin plus credentials is every site on the web reading the answers
	// that belong to a signed-in user.
	AllowCredentials bool

	// MaxAge is how long a browser may cache a preflight answer. Zero sends no
	// header and leaves the default of the browser standing. A negative value
	// sends 0, which asks the browser to cache nothing.
	MaxAge time.Duration
}

// CORS is [CORSWithConfig] with its default config, which allows every origin
// without credentials. Reach for [CORSWithConfig] as soon as the answer
// carries anything that belongs to one user.
//
// It is a middleware itself, so it goes into Use without a call:
//
//	r.Use(middleware.CORS[Ctx])
func CORS[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return CORSWithConfig[C](CORSConfig{AllowOrigins: []string{"*"}})(next)
}

// defaultCORSMethods is the method list of a preflight answer for a route that
// answers OPTIONS itself, where the router writes no Allow header.
var defaultCORSMethods = strings.Join([]string{
	http.MethodGet, http.MethodHead, http.MethodPost,
	http.MethodPut, http.MethodPatch, http.MethodDelete,
}, ", ")

// CORSWithConfig answers a preflight request and adds the CORS headers to
// every other answer.
//
// It panics on a config that cannot work: the origin "*" together with
// AllowCredentials, or an AllowOrigins entry that is not an origin. Both fail
// at startup rather than at the first request from a browser.
func CORSWithConfig[C router.Context](cfg CORSConfig) router.Middleware[C] {
	// The caller keeps its slice, so a later append to it cannot widen the
	// origins that this middleware allows.
	cfg.AllowOrigins = slices.Clone(cfg.AllowOrigins)
	wildcard := checkCORSOrigins(cfg.AllowOrigins, cfg.AllowCredentials)

	methods := strings.Join(cfg.AllowMethods, ", ")
	exposed := strings.Join(cfg.ExposeHeaders, ", ")
	allowed := strings.Join(cfg.AllowHeaders, ", ")

	// Zero and "send 0" are different answers, so the header value is built
	// only for a config that sends one.
	maxAge := ""
	switch {
	case cfg.MaxAge > 0:
		maxAge = strconv.Itoa(int(cfg.MaxAge.Seconds()))
	case cfg.MaxAge < 0:
		maxAge = "0"
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}
			req := c.Request()
			res := c.Response()
			origin := req.Header.Get(router.HeaderOrigin)

			router.AddVary(res.Header(), router.HeaderOrigin)
			if origin == "" {
				return next(c)
			}
			allow, err := cfg.allows(c, origin)
			if err != nil {
				return err
			}
			if !allow {
				return next(c)
			}

			value := origin
			if wildcard && cfg.AllowOriginFunc == nil {
				value = "*"
			}
			res.Header().Set(router.HeaderAccessControlAllowOrigin, value)
			if cfg.AllowCredentials {
				res.Header().Set(router.HeaderAccessControlAllowCredentials, "true")
			}

			preflight := req.Method == http.MethodOptions &&
				req.Header.Get(router.HeaderAccessControlRequestMethod) != ""
			if !preflight {
				if exposed != "" {
					res.Header().Set(router.HeaderAccessControlExposeHeaders, exposed)
				}
				return next(c)
			}

			router.AddVary(res.Header(),
				router.HeaderAccessControlRequestMethod, router.HeaderAccessControlRequestHeaders)
			// The router sets Allow on the response before it dispatches the
			// OPTIONS chain, so the truthful method list is already here.
			res.Header().Set(router.HeaderAccessControlAllowMethods,
				cmp.Or(methods, res.Header().Get(router.HeaderAllow), defaultCORSMethods))

			if allowed != "" {
				res.Header().Set(router.HeaderAccessControlAllowHeaders, allowed)
			} else if asked := req.Header.Get(router.HeaderAccessControlRequestHeaders); asked != "" {
				res.Header().Set(router.HeaderAccessControlAllowHeaders, asked)
			}
			if maxAge != "" {
				res.Header().Set(router.HeaderAccessControlMaxAge, maxAge)
			}

			res.WriteHeader(http.StatusNoContent)
			return nil
		}
	}
}

// allows reports whether the origin may read the response.
func (cfg CORSConfig) allows(c router.Context, origin string) (bool, error) {
	if cfg.AllowOriginFunc != nil {
		return cfg.AllowOriginFunc(c, origin)
	}
	for _, o := range cfg.AllowOrigins {
		if o == "*" || strings.EqualFold(o, origin) {
			return true, nil
		}
	}
	return false, nil
}

// checkCORSOrigins rejects a config that cannot work and reports whether the
// list holds "*".
func checkCORSOrigins(origins []string, credentials bool) bool {
	wildcard := false
	for _, o := range origins {
		switch o {
		case "*":
			if credentials {
				panic(`middleware: CORS cannot combine the origin "*" with AllowCredentials, ` +
					`because that lets every site read the answers of a signed-in user; ` +
					`name the origins that may send credentials`)
			}
			wildcard = true
		default:
			if _, ok := originOf(o); !ok {
				panic("middleware: CORS got " + strconv.Quote(o) + " in AllowOrigins, which is not an origin; " +
					`write a scheme and a host, as in "https://app.example", and reach anything else through AllowOriginFunc`)
			}
		}
	}
	return wildcard
}
