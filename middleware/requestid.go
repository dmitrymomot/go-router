package middleware

import (
	"uuid"

	"github.com/dmitrymomot/go-router"
)

// RequestIDKey is the context key under which [RequestID] stores the
// identifier. Read it with [RequestIDFrom].
const RequestIDKey = "request_id"

// RequestID gives every request an identifier. It keeps the one that the
// X-Request-Id header carries, and generates a UUID version 7 otherwise. It
// stores the value on the context and echoes it in the response header.
//
// Trust an inbound identifier only behind a proxy that sets or clears the
// header, because a client can send any value.
func RequestID[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return func(c C) error {
		id := c.Request().Header.Get(router.HeaderXRequestID)
		if id == "" {
			id = uuid.NewV7().String()
		}
		c.Response().Header().Set(router.HeaderXRequestID, id)
		c.Set(RequestIDKey, id)
		return next(c)
	}
}

// RequestIDFrom returns the identifier that [RequestID] stored, or an empty
// string.
func RequestIDFrom[C router.Context](c C) string {
	s, _ := c.Value(RequestIDKey).(string)
	return s
}
