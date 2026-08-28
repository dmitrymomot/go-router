package middleware

import (
	"uuid"

	"github.com/dmitrymomot/go-router"
)

const RequestIDKey = "request_id"

type RequestIDConfig struct {
	Skip func(c router.Context) bool

	Header string

	Generator func() string

	IgnoreInbound bool
}

func RequestID[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return RequestIDWithConfig[C](RequestIDConfig{})(next)
}

func RequestIDWithConfig[C router.Context](cfg RequestIDConfig) router.Middleware[C] {
	if cfg.Header == "" {
		cfg.Header = router.HeaderXRequestID
	}
	if cfg.Generator == nil {
		cfg.Generator = func() string { return uuid.NewV7().String() }
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}
			id := ""
			if !cfg.IgnoreInbound {
				id = c.Request().Header.Get(cfg.Header)
			}
			if id == "" {
				id = cfg.Generator()
			}
			c.Response().Header().Set(cfg.Header, id)
			c.Set(RequestIDKey, id)
			return next(c)
		}
	}
}

func RequestIDFrom[C router.Context](c C) string {
	s, _ := c.Value(RequestIDKey).(string)
	return s
}
