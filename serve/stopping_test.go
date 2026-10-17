package serve_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router/serve"
)

// stopProbe answers 503 once the stop began, and 200 before.
func stopProbe() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-serve.Stopping(r.Context()):
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
}

func TestStoppingIsNilOutsideAServer(t *testing.T) {
	if serve.Stopping(context.Background()) != nil {
		t.Fatal("Stopping of a bare context is not nil")
	}
}

func TestStoppingClosesBeforeTheDelay(t *testing.T) {
	onDrain := make(chan struct{})
	s := start(t, stopProbe(), serve.Config{
		DrainDelay: time.Second,
		OnDrain:    func() { close(onDrain) },
	})
	cl := client(t)
	if r := await(t, call(cl, s.url("http", "/"))); r.status != http.StatusOK {
		t.Fatalf("before the stop: %+v, want 200", r)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- s.stop(t) }()
	<-onDrain
	if r := await(t, call(cl, s.url("http", "/"))); r.status != http.StatusServiceUnavailable {
		t.Fatalf("during the delay: %+v, want 503", r)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestStoppingClosesForEveryServerOfRunAll(t *testing.T) {
	onDrain := make(chan struct{})
	g := startAll(t,
		serve.Server{Handler: ok(), Config: serve.Config{
			DrainDelay: time.Second,
			OnDrain:    func() { close(onDrain) },
		}},
		serve.Server{Handler: stopProbe()},
	)
	cl := client(t)
	if r := await(t, call(cl, g.url(1, "/"))); r.status != http.StatusOK {
		t.Fatalf("before the stop: %+v, want 200", r)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- g.stop(t) }()
	<-onDrain
	if r := await(t, call(cl, g.url(1, "/"))); r.status != http.StatusServiceUnavailable {
		t.Fatalf("the private server while the public one waits: %+v, want 503", r)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestStoppingReachesTheBackgroundFunctions(t *testing.T) {
	saw := make(chan struct{})
	s := start(t, ok(), serve.Config{
		Background: []func(context.Context) error{func(ctx context.Context) error {
			<-serve.Stopping(ctx)
			close(saw)
			<-ctx.Done()
			return nil
		}},
	})
	if err := s.stop(t); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	select {
	case <-saw:
	default:
		t.Fatal("the background context never reported the stop")
	}
}
