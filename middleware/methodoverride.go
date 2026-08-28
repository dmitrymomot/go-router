package middleware

import (
	"net/http"
	"strings"

	"github.com/dmitrymomot/go-router"
)

type MethodOverrideConfig struct {
	Skip   func(c router.Context) bool
	Getter func(c router.Context) string
}

func MethodOverride[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return MethodOverrideWithConfig[C](MethodOverrideConfig{})(next)
}

func MethodOverrideWithConfig[C router.Context](cfg MethodOverrideConfig) router.Middleware[C] {
	if cfg.Getter == nil {
		cfg.Getter = MethodFromHeader(router.HeaderXHTTPMethodOverride)
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			req := c.Request()
			if req.Method != http.MethodPost || skipped(cfg.Skip, c) {
				return next(c)
			}

			method := strings.ToUpper(strings.TrimSpace(cfg.Getter(c)))
			if !overridable(method) {
				return next(c)
			}

			overridden := *req
			overridden.Method = method
			c.SetRequest(&overridden)
			return next(c)
		}
	}
}

func MethodFromHeader(name string) func(router.Context) string {
	return func(c router.Context) string { return c.Request().Header.Get(name) }
}

func MethodFromForm(field string) func(router.Context) string {
	return func(c router.Context) string {
		if r, ok := c.(formReader); ok {
			return r.FormValue(field)
		}
		req := c.Request()
		if req.Body != nil {
			req.Body = http.MaxBytesReader(c.Response(), req.Body, router.DefaultMaxBodyBytes)
		}
		return req.PostFormValue(field)
	}
}

func MethodFromQuery(param string) func(router.Context) string {
	return func(c router.Context) string { return c.Request().URL.Query().Get(param) }
}

type formReader interface{ FormValue(name string) string }

func overridable(m string) bool {
	switch m {
	case http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
