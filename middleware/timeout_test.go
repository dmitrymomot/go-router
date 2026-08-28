package middleware_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func timeoutRouter(cfg middleware.TimeoutConfig) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.TimeoutWithConfig[*appContext](cfg))
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
	r.Use(middleware.TimeoutWithConfig[*appContext](middleware.TimeoutConfig{Duration: time.Minute}))
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

func TestTimeoutWithConfigNeedsADuration(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		func() {
			defer func() {
				msg, ok := recover().(string)
				if !ok || !strings.Contains(msg, "Duration") {
					t.Errorf("recovered %v for a duration of %s, want a panic that names it", msg, d)
				}
			}()
			middleware.TimeoutWithConfig[*appContext](middleware.TimeoutConfig{Duration: d})
		}()
	}
}

func TestTimeoutOnTimeoutReplacesTheAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var caught error
		r := timeoutRouter(middleware.TimeoutConfig{
			Duration: 20 * time.Millisecond,
			Status:   http.StatusGatewayTimeout,
			OnTimeout: func(c router.Context, err error) error {
				caught = err
				return router.ErrServiceUnavailable.WithMessage("the report is not ready")
			},
		})

		rec := get(r, "/slow")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want the 503 of OnTimeout and not the configured 504", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "the report is not ready") {
			t.Errorf("body = %q, want the message of OnTimeout", rec.Body.String())
		}
		if !errors.Is(caught, context.DeadlineExceeded) {
			t.Errorf("OnTimeout received %v, want an error that names the deadline", caught)
		}
	})
}

func TestTimeoutOnTimeoutSeesTheErrorOfTheHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sentinel := errors.New("the query was cut short")
		var caught error

		r := newRouter()
		r.Use(middleware.TimeoutWithConfig[*appContext](middleware.TimeoutConfig{
			Duration: 20 * time.Millisecond,
			OnTimeout: func(c router.Context, err error) error {
				caught = err
				return router.ErrGatewayTimeout
			},
		}))
		r.GET("/slow", func(c *appContext) error {
			<-c.Done()
			return sentinel
		})

		if rec := get(r, "/slow"); rec.Code != http.StatusGatewayTimeout {
			t.Fatalf("status = %d, want 504", rec.Code)
		}
		if !errors.Is(caught, sentinel) {
			t.Errorf("OnTimeout received %v, want the error of the handler in it", caught)
		}
	})
}

// timeoutFailingRouter answers /boom with a plain server fault under a deadline
// that never fires, and reports what the router logged for it.
func timeoutFailingRouter() (*router.Router[*appContext], *bytes.Buffer) {
	var buf bytes.Buffer
	r := newRouter()
	r.Logger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	r.Use(middleware.TimeoutWithConfig[*appContext](middleware.TimeoutConfig{Duration: 30 * time.Second}))
	r.GET("/boom", func(*appContext) error { return errors.New("the database is on fire") })
	return r, &buf
}

// TestTimeoutKeepsAServerFaultAtErrorLevel pins the level of a 500 that has
// nothing to do with the deadline.
//
// The router logs a failed request at debug level when the context of the
// request is cancelled, because that is a client that went away rather than a
// server fault. A middleware that leaves its own cancelled context behind
// takes every 500 under it out of the 5xx alerts.
func TestTimeoutKeepsAServerFaultAtErrorLevel(t *testing.T) {
	r, buf := timeoutFailingRouter()

	if rec := get(r, "/boom"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("the 500 logged as %q, want level=ERROR", buf.String())
	}
}

func TestTimeoutHandsTheErrorHandlerALiveContext(t *testing.T) {
	var seen error
	r := newRouter()
	r.ErrorHandler(func(c *appContext, _ error) { seen = c.Err() })
	r.Use(middleware.TimeoutWithConfig[*appContext](middleware.TimeoutConfig{Duration: 30 * time.Second}))
	r.GET("/boom", func(*appContext) error { return errors.New("the database is on fire") })

	get(r, "/boom")
	if seen != nil {
		t.Errorf("the error handler read c.Err() = %v, want none: the deadline never fired", seen)
	}
}

// TestTimeoutStillReportsAClientThatWentAway is the other half of the pair: the
// request that the middleware puts back has to carry the cancellation of the
// server, or a client that hung up would read as a server fault.
func TestTimeoutStillReportsAClientThatWentAway(t *testing.T) {
	r, buf := timeoutFailingRouter()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	do(r, httptest.NewRequest(http.MethodGet, "/boom", nil).WithContext(ctx))

	if !strings.Contains(buf.String(), "level=DEBUG") {
		t.Errorf("the failed request logged as %q, want level=DEBUG for a client that went away", buf.String())
	}
}
