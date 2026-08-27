package middleware_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func timeoutRouter(cfg middleware.TimeoutConfig) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.Timeout(cfg).Middleware)
	r.GET("/slow", func(c *appContext) error {
		<-c.Done()
		return c.Err()
	})
	r.GET("/fast", func(c *appContext) error { return c.String(http.StatusOK, "fast") })
	return r
}

func TestTimeout(t *testing.T) {
	// synctest runs the deadline on a fake clock, so the test finishes at once
	// and never depends on the speed of the machine.
	synctest.Test(t, func(t *testing.T) {
		r := timeoutRouter(middleware.TimeoutConfig{Duration: 20 * time.Millisecond})

		if rec := get(r, "/slow"); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
		if rec := get(r, "/fast"); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})
}

func TestTimeoutCustomStatus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := timeoutRouter(middleware.TimeoutConfig{
			Duration: 20 * time.Millisecond,
			Status:   http.StatusGatewayTimeout,
			Message:  "took too long",
		})

		rec := get(r, "/slow")
		if rec.Code != http.StatusGatewayTimeout {
			t.Fatalf("status = %d, want 504", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "took too long") {
			t.Errorf("body = %q, want the configured message", rec.Body.String())
		}
	})
}

func TestTimeoutSkip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := timeoutRouter(middleware.TimeoutConfig{
			Duration: 20 * time.Millisecond,
			Skip:     skipPath("/fast"),
		})
		r.GET("/nodeadline", func(c *appContext) error {
			if _, ok := c.Request().Context().Deadline(); ok {
				return router.ErrInternalServerError.WithMessage("a deadline was set")
			}
			return c.NoContent(http.StatusOK)
		})

		if rec := get(r, "/fast"); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})
}

func TestTimeoutPassesTheDeadlineToTheHandler(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Timeout(middleware.TimeoutConfig{Duration: time.Minute}).Middleware)
	r.GET("/", func(c *appContext) error {
		if _, ok := c.Request().Context().Deadline(); !ok {
			return router.ErrInternalServerError.WithMessage("no deadline")
		}
		var _ context.Context = c
		return c.NoContent(http.StatusOK)
	})

	if rec := get(r, "/"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestTimeoutZeroDurationIsOff(t *testing.T) {
	r := timeoutRouter(middleware.TimeoutConfig{})
	if rec := get(r, "/fast"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}
