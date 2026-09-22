package middleware

import (
	"cmp"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
)

// CORSConfig configures [CORSWithConfig].
//
// AllowOrigins names the origins that may read the answers, each with a scheme
// and a host; "*" allows every origin. AllowOriginFunc decides per request in
// its place, for a list that lives in a database. One of the two is required,
// and only one.
//
// AllowMethods and AllowHeaders answer a preflight; empty ones take the
// methods of the route and the headers the client asked for. ExposeHeaders
// names the response headers the browser hands to the script.
// AllowCredentials lets the browser send cookies, and it cannot go with "*".
// MaxAge is how long the browser caches the preflight, and a negative one
// caches nothing.
type CORSConfig[C router.Context] struct {
	Skip             func(c C) bool
	AllowOrigins     []string
	AllowOriginFunc  func(c C, origin string) (bool, error)
	AllowMethods     []string
	AllowHeaders     []string
	ExposeHeaders    []string
	AllowCredentials bool
	MaxAge           time.Duration
}

// CORS allows every origin, without credentials. It suits a public read-only
// API; anything that carries a session needs [CORSWithConfig] with the origins
// named.
//
// See Order in the package doc for where it goes.
func CORS[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return CORSWithConfig(CORSConfig[C]{AllowOrigins: []string{"*"}})(next)
}

var defaultCORSMethods = strings.Join([]string{
	http.MethodGet, http.MethodHead, http.MethodPost,
	http.MethodPut, http.MethodPatch, http.MethodDelete,
}, ", ")

// CORSWithConfig is [CORS] with a configuration. It answers a preflight itself
// with a 204 and never calls the handler.
//
// CORSWithConfig panics when neither AllowOrigins nor AllowOriginFunc is set,
// and when both are. It panics on an entry of AllowOrigins that is not an
// origin, and on "*" together with AllowCredentials, which would let every
// site read the answers of a signed-in user.
func CORSWithConfig[C router.Context](cfg CORSConfig[C]) router.Middleware[C] {
	switch {
	case len(cfg.AllowOrigins) == 0 && cfg.AllowOriginFunc == nil:
		panic("middleware: CORSWithConfig needs AllowOrigins or AllowOriginFunc")
	case len(cfg.AllowOrigins) > 0 && cfg.AllowOriginFunc != nil:
		panic("middleware: CORSWithConfig takes AllowOrigins or AllowOriginFunc, not both")
	}
	origins, wildcard := checkOrigins("CORSConfig.AllowOrigins", cfg.AllowOrigins, true,
		", and reach anything else through AllowOriginFunc")
	if wildcard && cfg.AllowCredentials {
		panic(`middleware: CORS cannot combine the origin "*" with AllowCredentials, ` +
			`because that lets every site read the answers of a signed-in user; ` +
			`name the origins that may send credentials`)
	}
	cfg.AllowOrigins = origins

	methods := strings.Join(cfg.AllowMethods, ", ")
	exposed := strings.Join(cfg.ExposeHeaders, ", ")
	allowed := strings.Join(cfg.AllowHeaders, ", ")

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
			if wildcard {
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

func (cfg CORSConfig[C]) allows(c C, origin string) (bool, error) {
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
