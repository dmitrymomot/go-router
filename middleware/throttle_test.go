package middleware_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// blockingRouter holds each request on /slow until release is closed.
func blockingRouter(cfg middleware.ThrottleConfig[*appContext]) (r *router.Router[*appContext], started chan struct{}, release chan struct{}) {
	started, release = make(chan struct{}, 16), make(chan struct{})
	r = newRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.Use(middleware.ThrottleWithConfig(cfg))
	r.GET("/slow", func(c *appContext) error {
		started <- struct{}{}
		<-release
		return c.String(http.StatusOK, "done")
	})
	return r, started, release
}

func TestThrottleRejectsBeyondTheLimit(t *testing.T) {
	r, started, release := blockingRouter(middleware.ThrottleConfig[*appContext]{
		Limit: 2, RetryAfter: 1500 * time.Millisecond,
	})
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { get(r, "/slow") })
	}
	<-started
	<-started

	rec := get(r, "/slow")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get(router.HeaderRetryAfter); got != "2" {
		t.Errorf("Retry-After = %q, want 2", got)
	}
	close(release)
	wg.Wait()

	if rec := get(r, "/slow"); rec.Code != http.StatusOK {
		t.Fatalf("after the others finished: status = %d, want 200", rec.Code)
	}
}

func TestThrottleBacklogWaitsForASlot(t *testing.T) {
	r, started, release := blockingRouter(middleware.ThrottleConfig[*appContext]{
		Limit: 1, Backlog: 1, BacklogTimeout: 5 * time.Second,
	})
	codes := make(chan int, 2)
	go func() { codes <- get(r, "/slow").Code }()
	<-started
	go func() { codes <- get(r, "/slow").Code }()

	// Give the second request time to take the one place in the backlog; the
	// third then finds the limit and the backlog both full.
	time.Sleep(100 * time.Millisecond)
	if rec := get(r, "/slow"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third request = %d, want 429", rec.Code)
	}
	close(release)
	for range 2 {
		if code := <-codes; code != http.StatusOK {
			t.Errorf("queued request = %d, want 200", code)
		}
	}
}

func TestThrottleBacklogTimeoutAnd503(t *testing.T) {
	r, started, release := blockingRouter(middleware.ThrottleConfig[*appContext]{
		Limit: 1, Backlog: 1, BacklogTimeout: 20 * time.Millisecond, Status: http.StatusServiceUnavailable,
	})
	defer close(release)
	go get(r, "/slow")
	<-started
	if rec := get(r, "/slow"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestThrottleContextEndsTheWait(t *testing.T) {
	r, started, release := blockingRouter(middleware.ThrottleConfig[*appContext]{
		Limit: 1, Backlog: 1, BacklogTimeout: time.Minute,
	})
	defer close(release)
	go get(r, "/slow")
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	req := httptest.NewRequest(http.MethodGet, "/slow", nil).WithContext(ctx)
	if rec := do(r, req); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
}

func TestThrottleOnRejectAndSkip(t *testing.T) {
	r, started, release := blockingRouter(middleware.ThrottleConfig[*appContext]{
		Limit: 1,
		OnReject: func(_ *appContext, err error) error {
			if !errors.Is(err, router.ErrTooManyRequests) {
				return fmt.Errorf("OnReject got %v, want the 429", err)
			}
			return router.ErrBadGateway
		},
		Skip: func(c *appContext) bool { return c.Request().Header.Get("X-Skip") != "" },
	})
	defer close(release)
	go get(r, "/slow")
	<-started

	if rec := get(r, "/slow"); rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/slow", nil)
	req.Header.Set("X-Skip", "1")
	go do(r, req)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("a skipped request was throttled")
	}
}

func TestThrottlePanics(t *testing.T) {
	tests := map[string]func(){
		"zero limit":       func() { middleware.Throttle[*appContext](0) },
		"negative backlog": func() { middleware.ThrottleWithConfig(middleware.ThrottleConfig[*appContext]{Limit: 1, Backlog: -1}) },
		"negative timeout": func() {
			middleware.ThrottleWithConfig(middleware.ThrottleConfig[*appContext]{Limit: 1, BacklogTimeout: -1})
		},
		"negative retry": func() {
			middleware.ThrottleWithConfig(middleware.ThrottleConfig[*appContext]{Limit: 1, RetryAfter: -1})
		},
		"bad status": func() { middleware.ThrottleWithConfig(middleware.ThrottleConfig[*appContext]{Limit: 1, Status: 500}) },
	}
	for name, f := range tests {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("did not panic")
				}
			}()
			f()
		})
	}
}
