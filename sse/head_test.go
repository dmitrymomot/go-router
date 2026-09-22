package sse_test

import (
	"net/http"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/sse"
)

// A stream decides HEAD in Open, so it answers to the rule of every response
// path on its own.
func TestHEADMatchesGET(t *testing.T) {
	r := router.New(newContext)
	r.GET("/events", func(c *Context) error {
		s, err := sse.Open(c, http.StatusOK)
		if err != nil {
			return err
		}
		return s.Send(sse.Event{Data: "hello"})
	})

	routertest.AssertHEADMatchesGET(t, r, "/events")
}
