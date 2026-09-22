package middleware

import (
	"uuid"

	"github.com/dmitrymomot/go-router"
)

// RequestIDKey is where [RequestID] stores the id on the context.
const RequestIDKey = "request_id"

// MaxRequestIDLength is the longest id [RequestID] takes from a client.
const MaxRequestIDLength = 128

// RequestIDConfig configures [RequestIDWithConfig]. Header is the header to
// read and write, and an empty one takes [router.HeaderXRequestID]. Generator
// builds an id, and a nil one takes a UUIDv7. IgnoreInbound drops the id the
// client sent and always generates one, which suits a public endpoint.
type RequestIDConfig[C router.Context] struct {
	Skip          func(c C) bool
	Header        string
	Generator     func() string
	IgnoreInbound bool
}

// RequestID gives each request an id: the one the client sent, or a new UUIDv7.
// The id goes into the X-Request-Id header of the response and onto the
// context, where [RequestIDFrom] reads it.
//
// An id from the client counts only when it has 1 to [MaxRequestIDLength]
// letters, digits, dots, dashes and underscores; any other gets a new id, so
// a client cannot put arbitrary bytes into the logs.
//
// See Order in the package doc for where it goes.
func RequestID[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return RequestIDWithConfig(RequestIDConfig[C]{})(next)
}

// RequestIDWithConfig is [RequestID] with a configuration.
func RequestIDWithConfig[C router.Context](cfg RequestIDConfig[C]) router.Middleware[C] {
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
			if !validRequestID(id) {
				id = cfg.Generator()
			}
			c.Response().Header().Set(cfg.Header, id)
			c.Set(RequestIDKey, id)
			return next(c)
		}
	}
}

// RequestIDFrom reports the id that [RequestID] stored, or "" when the
// middleware did not run.
func RequestIDFrom(c router.Context) string {
	s, _ := c.Value(RequestIDKey).(string)
	return s
}

func validRequestID(id string) bool {
	if id == "" || len(id) > MaxRequestIDLength {
		return false
	}
	for i := range len(id) {
		switch b := id[i]; {
		case 'A' <= b && b <= 'Z', 'a' <= b && b <= 'z', '0' <= b && b <= '9', b == '.', b == '_', b == '-':
		default:
			return false
		}
	}
	return true
}
