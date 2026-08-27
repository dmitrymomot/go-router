package middleware

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
)

// CORSConfig configures [CORSConfig.Middleware].
type CORSConfig struct {
	// AllowOrigins lists the origins that may read the response. A single
	// entry "*" allows every origin. An empty list allows none.
	AllowOrigins []string

	// AllowOriginFunc decides per origin. It takes precedence over
	// AllowOrigins.
	AllowOriginFunc func(origin string) bool

	// AllowMethods lists the methods of a preflight answer. It defaults to the
	// safe and the common write methods.
	AllowMethods []string

	// AllowHeaders lists the request headers that a preflight answer permits.
	// An empty list echoes the headers that the preflight asked for.
	AllowHeaders []string

	// ExposeHeaders lists the response headers that the browser reveals to the
	// script.
	ExposeHeaders []string

	// AllowCredentials permits cookies and the Authorization header. It cannot
	// combine with the origin "*", so the middleware echoes the request origin
	// instead.
	AllowCredentials bool

	// MaxAge is how long a browser may cache a preflight answer.
	MaxAge time.Duration
}

// CORS returns a configuration that allows the named origins. Pass "*" to
// allow every origin.
func CORS(origins ...string) CORSConfig {
	return CORSConfig{AllowOrigins: origins}
}

// defaultCORSMethods is the method list of a preflight answer.
var defaultCORSMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost,
	http.MethodPut, http.MethodPatch, http.MethodDelete,
}

// Middleware answers a preflight request and adds the CORS headers to every
// other answer.
func (cfg CORSConfig) Middleware[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	allowMethods := cfg.AllowMethods
	if len(allowMethods) == 0 {
		allowMethods = defaultCORSMethods
	}
	methods := strings.Join(allowMethods, ", ")
	exposed := strings.Join(cfg.ExposeHeaders, ", ")
	allowed := strings.Join(cfg.AllowHeaders, ", ")
	maxAge := strconv.Itoa(int(cfg.MaxAge.Seconds()))

	return func(c C) error {
		req := c.Request()
		res := c.Response()
		origin := req.Header.Get(router.HeaderOrigin)

		res.Header().Add(router.HeaderVary, router.HeaderOrigin)
		if origin == "" || !cfg.allows(origin) {
			return next(c)
		}

		value := origin
		if !cfg.AllowCredentials && slices.Contains(cfg.AllowOrigins, "*") && cfg.AllowOriginFunc == nil {
			value = "*"
		}
		res.Header().Set(router.HeaderAccessControlAllowOrigin, value)
		if cfg.AllowCredentials {
			res.Header().Set(router.HeaderAccessControlAllowCredentials, "true")
		}
		if exposed != "" {
			res.Header().Set(router.HeaderAccessControlExposeHeaders, exposed)
		}

		preflight := req.Method == http.MethodOptions &&
			req.Header.Get(router.HeaderAccessControlRequestMethod) != ""
		if !preflight {
			return next(c)
		}

		res.Header().Add(router.HeaderVary, router.HeaderAccessControlRequestMethod)
		res.Header().Add(router.HeaderVary, router.HeaderAccessControlRequestHeaders)
		res.Header().Set(router.HeaderAccessControlAllowMethods, methods)

		if allowed != "" {
			res.Header().Set(router.HeaderAccessControlAllowHeaders, allowed)
		} else if asked := req.Header.Get(router.HeaderAccessControlRequestHeaders); asked != "" {
			res.Header().Set(router.HeaderAccessControlAllowHeaders, asked)
		}
		if cfg.MaxAge > 0 {
			res.Header().Set(router.HeaderAccessControlMaxAge, maxAge)
		}

		res.WriteHeader(http.StatusNoContent)
		return nil
	}
}

// allows reports whether the origin may read the response.
func (cfg CORSConfig) allows(origin string) bool {
	if cfg.AllowOriginFunc != nil {
		return cfg.AllowOriginFunc(origin)
	}
	for _, o := range cfg.AllowOrigins {
		if o == "*" || strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}
