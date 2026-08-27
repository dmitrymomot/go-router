package middleware

import (
	"uuid"

	"github.com/dmitrymomot/go-router"
)

// RequestIDKey is the context key under which [RequestIDConfig.Middleware]
// stores the identifier. Read it with [RequestIDFrom].
const RequestIDKey = "request_id"

// RequestIDConfig configures [RequestIDConfig.Middleware].
type RequestIDConfig struct {
	// Skip passes a request straight to the next handler when it returns true.
	Skip func(c router.Context) bool

	// Header is the request and response header that carries the identifier.
	// It defaults to X-Request-Id.
	Header string

	// Generator builds an identifier. It defaults to a UUID version 7, which
	// sorts by creation time.
	Generator func() string

	// IgnoreInbound drops the identifier that the request carried and always
	// generates one. Turn it on when the server faces clients directly,
	// because a client can put any value in the header.
	IgnoreInbound bool
}

// RequestID fills in the defaults of the config and returns it.
func RequestID(cfg RequestIDConfig) RequestIDConfig {
	if cfg.Header == "" {
		cfg.Header = router.HeaderXRequestID
	}
	if cfg.Generator == nil {
		cfg.Generator = func() string { return uuid.NewV7().String() }
	}
	return cfg
}

// Middleware gives every request an identifier. It keeps the one that the
// request carried unless IgnoreInbound is set, stores the value on the context
// and echoes it in the response header.
func (cfg RequestIDConfig) Middleware[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	cfg = RequestID(cfg)
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

// RequestIDFrom returns the identifier that [RequestIDConfig.Middleware]
// stored, or an empty string.
func RequestIDFrom[C router.Context](c C) string {
	s, _ := c.Value(RequestIDKey).(string)
	return s
}
