package serve_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router/serve"
)

type ctxKey struct{}

func TestBackgroundRunsWithTheValuesOfTheContext(t *testing.T) {
	got := make(chan any, 1)
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "v"))
	errc := make(chan error, 1)
	go func() {
		errc <- serve.Run(ctx, ok(), serve.Config{
			Addr: "127.0.0.1:0",
			Background: []func(context.Context) error{func(ctx context.Context) error {
				got <- ctx.Value(ctxKey{})
				<-ctx.Done()
				return ctx.Err()
			}},
		})
	}()

	select {
	case v := <-got:
		if v != "v" {
			t.Fatalf("the value = %v, want v", v)
		}
	case <-time.After(wait):
		t.Fatal("the background function never ran")
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("err = %v, want nil: context.Canceled after the cancel is a clean stop", err)
	}
}

func TestBackgroundStopsAfterTheDrain(t *testing.T) {
	b := newBlocker(t)
	var cancelled atomic.Bool
	sawDrain := make(chan struct{})
	s := start(t, b, serve.Config{
		Background: []func(context.Context) error{func(ctx context.Context) error {
			<-serve.Draining(ctx)
			close(sawDrain)
			<-ctx.Done()
			cancelled.Store(true)
			return nil
		}},
	})

	res := call(client(t), s.url("http", "/"))
	b.running(t)
	stopped := make(chan error, 1)
	go func() { stopped <- s.stop(t) }()

	select {
	case <-sawDrain:
	case <-time.After(wait):
		t.Fatal("the background context never reported the drain")
	}
	time.Sleep(50 * time.Millisecond)
	if cancelled.Load() {
		t.Fatal("the background context ended while a request was still in flight")
	}
	b.free()
	if r := await(t, res); r.err != nil || r.body != "late" {
		t.Fatalf("the request = %+v, want late", r)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !cancelled.Load() {
		t.Fatal("Run returned before the background function stopped")
	}
}

func TestABackgroundFailureStopsTheServer(t *testing.T) {
	boom := errors.New("boom")
	fail := make(chan struct{})
	var drained atomic.Bool
	s := start(t, ok(), serve.Config{
		OnDrain: func() { drained.Store(true) },
		Background: []func(context.Context) error{
			func(context.Context) error {
				<-fail
				return boom
			},
			func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
		},
	})

	close(fail)
	select {
	case err := <-s.errc:
		s.stopped = true
		if err != boom { //nolint:errorlint // Run reports the error as it stands.
			t.Fatalf("err = %v, want boom itself", err)
		}
	case <-time.After(wait):
		t.Fatal("Run did not return after a background function failed")
	}
	if !drained.Load() {
		t.Fatal("the server stopped without OnDrain")
	}
}

func TestABackgroundFunctionThatEndsStopsNothing(t *testing.T) {
	done := make(chan struct{})
	s := start(t, ok(), serve.Config{
		Background: []func(context.Context) error{func(context.Context) error {
			defer close(done)
			return nil
		}},
	})
	<-done
	if r := await(t, call(client(t), s.url("http", "/"))); r.status != http.StatusOK {
		t.Fatalf("the request = %+v, want 200", r)
	}
	if err := s.stop(t); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestBackgroundReportsAnErrorAfterTheCancel(t *testing.T) {
	late := errors.New("flush failed")
	s := start(t, ok(), serve.Config{
		Background: []func(context.Context) error{func(ctx context.Context) error {
			<-ctx.Done()
			return late
		}},
	})
	if err := s.stop(t); !errors.Is(err, late) {
		t.Fatalf("err = %v, want the error the function reported", err)
	}
}

func TestBackgroundReportsItsTimeout(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	s := start(t, ok(), serve.Config{
		ShutdownTimeout: 50 * time.Millisecond,
		Background: []func(context.Context) error{func(context.Context) error {
			<-release
			return nil
		}},
	})
	err := s.stop(t)
	if err == nil || !strings.Contains(err.Error(), "did not stop in 50ms") {
		t.Fatalf("err = %v, want the timeout of the background functions", err)
	}
}

func TestANegativeShutdownTimeoutWaitsForNoBackgroundFunction(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	s := start(t, ok(), serve.Config{
		ShutdownTimeout: -1,
		Background: []func(context.Context) error{func(context.Context) error {
			<-release
			return nil
		}},
	})
	if err := s.stop(t); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestRunRejectsANilBackgroundFunction(t *testing.T) {
	err := serve.Run(context.Background(), ok(), serve.Config{
		Addr:       "127.0.0.1:0",
		Background: []func(context.Context) error{func(context.Context) error { return nil }, nil},
	})
	if err == nil || !strings.Contains(err.Error(), "Config.Background[1] is nil") {
		t.Fatalf("err = %v, want the nil function named", err)
	}
}

func TestBackgroundDoesNotStartForAContextAlreadyDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ran atomic.Bool
	err := serve.Run(ctx, ok(), serve.Config{
		Addr: "127.0.0.1:0",
		Background: []func(context.Context) error{func(context.Context) error {
			ran.Store(true)
			return nil
		}},
	})
	if err != nil || ran.Load() {
		t.Fatalf("err = %v, ran = %v; want nil and no run", err, ran.Load())
	}
}

func TestRunAllStopsEveryServerWhenABackgroundFunctionFails(t *testing.T) {
	boom := errors.New("boom")
	fail := make(chan struct{})
	g := startAll(t,
		serve.Server{Handler: ok()},
		serve.Server{Handler: ok(), Config: serve.Config{
			Background: []func(context.Context) error{func(context.Context) error {
				<-fail
				return boom
			}},
		}},
	)
	close(fail)
	err := g.wait(t)
	if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "serve: server 1: ") {
		t.Fatalf("err = %v, want boom named for server 1", err)
	}
}
