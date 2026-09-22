package serve_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router/serve"
)

type group struct {
	cancel   context.CancelFunc
	errc     chan error
	err      error
	addrs    []string
	returned bool
}

// startAll runs RunAll on servers and waits until every one of them listens.
func startAll(tb testing.TB, servers ...serve.Server) *group {
	tb.Helper()

	bound := make([]chan net.Addr, len(servers))
	for i := range servers {
		cfg := &servers[i].Config
		if cfg.Addr == "" && cfg.Listener == nil {
			cfg.Addr = "127.0.0.1:0"
		}
		if cfg.Logger == nil {
			cfg.Logger = slog.New(slog.DiscardHandler)
		}
		bound[i] = make(chan net.Addr, 1)
		onListen := cfg.OnListen
		cfg.OnListen = func(a net.Addr) {
			if onListen != nil {
				onListen(a)
			}
			bound[i] <- a
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	g := &group{cancel: cancel, errc: make(chan error, 1), addrs: make([]string, len(servers))}
	go func() { g.errc <- serve.RunAll(ctx, servers...) }()

	tb.Cleanup(func() {
		if g.returned {
			return
		}
		cancel()
		select {
		case <-g.errc:
		case <-time.After(wait):
			tb.Error("RunAll did not return during the cleanup")
		}
	})

	for i := range servers {
		select {
		case a := <-bound[i]:
			g.addrs[i] = a.String()
		case err := <-g.errc:
			// Every OnListen runs before any server serves, so a server that
			// failed at once still left every address behind.
			g.err, g.returned = err, true
			for j := i; j < len(servers); j++ {
				select {
				case a := <-bound[j]:
					g.addrs[j] = a.String()
				default:
					tb.Fatalf("RunAll returned before server %d listened: %v", j, err)
				}
			}
			return g
		case <-time.After(wait):
			tb.Fatalf("server %d did not listen", i)
		}
	}
	return g
}

// wait gives back what RunAll returned, without cancelling its context.
func (g *group) wait(tb testing.TB) error {
	tb.Helper()
	if g.returned {
		return g.err
	}
	select {
	case err := <-g.errc:
		g.err, g.returned = err, true
		return err
	case <-time.After(wait):
		tb.Fatal("RunAll did not return")
		return nil
	}
}

func (g *group) stop(tb testing.TB) error {
	tb.Helper()
	g.cancel()
	return g.wait(tb)
}

func (g *group) url(i int, path string) string {
	return "http://" + g.addrs[i] + path
}

func TestRunAllNeedsAContext(t *testing.T) {
	var ctx context.Context
	if err := serve.RunAll(ctx, serve.Server{Handler: ok(), Config: serve.Config{Addr: "127.0.0.1:0"}}); err == nil {
		t.Fatal("RunAll accepted a nil context")
	}
}

func TestRunAllNeedsAServer(t *testing.T) {
	if err := serve.RunAll(context.Background()); err == nil {
		t.Fatal("RunAll accepted no server")
	}
}

func TestRunAllServesEveryServer(t *testing.T) {
	g := startAll(t, serve.Server{Handler: ok()}, serve.Server{Handler: ok()})

	for i := range g.addrs {
		res := await(t, call(client(t), g.url(i, "/")))
		if res.err != nil {
			t.Fatalf("request to server %d: %v", i, res.err)
		}
		if res.status != http.StatusOK || res.body != "ok" {
			t.Fatalf("server %d answered %d %q, want 200 %q", i, res.status, res.body, "ok")
		}
	}
	if err := g.stop(t); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestRunAllNamesTheServerOfAnError(t *testing.T) {
	t.Run("a check", func(t *testing.T) {
		err := serve.RunAll(context.Background(),
			serve.Server{Handler: ok(), Config: serve.Config{Addr: "127.0.0.1:0"}},
			serve.Server{Config: serve.Config{Addr: "127.0.0.1:0"}},
		)
		if err == nil {
			t.Fatal("RunAll accepted a nil handler")
		}
		if !strings.HasPrefix(err.Error(), "serve: server 1: ") {
			t.Errorf("err = %q, want it to name server 1", err)
		}
		if strings.Contains(err.Error(), "serve: serve:") {
			t.Errorf("err = %q, names the package twice", err)
		}
	})

	t.Run("an error of OnServer", func(t *testing.T) {
		boom := errors.New("boom")
		err := serve.RunAll(context.Background(),
			serve.Server{Handler: ok(), Config: serve.Config{Addr: "127.0.0.1:0"}},
			serve.Server{Handler: ok(), Config: serve.Config{
				Addr:     "127.0.0.1:0",
				OnServer: func(*http.Server) error { return boom },
			}},
		)
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the error of OnServer", err)
		}
		if want := "serve: server 1: boom"; err.Error() != want {
			t.Errorf("err = %q, want %q", err, want)
		}
	})
}

func TestRunAllOpensEveryListenerBeforeServingAny(t *testing.T) {
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick an address: %v", err)
	}
	addr := free.Addr().String()
	free.Close() //nolint:errcheck // Only the address was wanted.

	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("take an address: %v", err)
	}
	defer taken.Close() //nolint:errcheck // The test is done with it.

	listened := false
	err = serve.RunAll(context.Background(),
		serve.Server{Handler: ok(), Config: serve.Config{
			Addr:     addr,
			Logger:   slog.New(slog.DiscardHandler),
			OnListen: func(net.Addr) { listened = true },
		}},
		serve.Server{Handler: ok(), Config: serve.Config{Addr: taken.Addr().String()}},
	)
	if err == nil || !strings.HasPrefix(err.Error(), "serve: server 1: ") {
		t.Fatalf("err = %v, want the listen failure of server 1", err)
	}
	if listened {
		t.Error("server 0 listened although server 1 could not")
	}

	again, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("RunAll left the listener of server 0 open: %v", err)
	}
	again.Close() //nolint:errcheck // The test is done with it.
}

func TestRunAllChecksEveryServerBeforeOpeningAny(t *testing.T) {
	listened, drained := false, false
	done := make(chan error, 1)
	go func() {
		done <- serve.RunAll(context.Background(),
			serve.Server{Handler: ok(), Config: serve.Config{
				Addr:       "127.0.0.1:0",
				DrainDelay: time.Minute,
				Logger:     slog.New(slog.DiscardHandler),
				OnListen:   func(net.Addr) { listened = true },
				OnDrain:    func() { drained = true },
			}},
			serve.Server{Handler: ok(), Config: serve.Config{
				Addr:      "127.0.0.1:0",
				TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13},
			}},
		)
	}()

	select {
	case err := <-done:
		if err == nil || !strings.HasPrefix(err.Error(), "serve: server 1: ") {
			t.Fatalf("err = %v, want the TLS check of server 1", err)
		}
	case <-time.After(wait):
		t.Fatal("RunAll started server 0 before it checked server 1")
	}
	if listened || drained {
		t.Error("server 0 started although server 1 was wrong")
	}
}

func TestRunAllClosesEveryCallerListenerOnEveryPath(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := map[string]struct {
		run func(ln0, ln1 net.Listener) error
		// handed lists the listeners that RunAll was given.
		handed []int
	}{
		"cancelled context": {
			handed: []int{0, 1},
			run: func(ln0, ln1 net.Listener) error {
				return serve.RunAll(cancelled,
					serve.Server{Handler: ok(), Config: serve.Config{Listener: ln0}},
					serve.Server{Handler: ok(), Config: serve.Config{Listener: ln1}},
				)
			},
		},
		"nil handler in server 0": {
			handed: []int{0, 1},
			run: func(ln0, ln1 net.Listener) error {
				return serve.RunAll(context.Background(),
					serve.Server{Config: serve.Config{Listener: ln0}},
					serve.Server{Handler: ok(), Config: serve.Config{Listener: ln1}},
				)
			},
		},
		"TLS config without a certificate in server 1": {
			handed: []int{0, 1},
			run: func(ln0, ln1 net.Listener) error {
				return serve.RunAll(context.Background(),
					serve.Server{Handler: ok(), Config: serve.Config{Listener: ln0}},
					serve.Server{Handler: ok(), Config: serve.Config{
						Listener:  ln1,
						TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13},
					}},
				)
			},
		},
		"listen failure in server 1": {
			handed: []int{0},
			run: func(ln0, ln1 net.Listener) error {
				return serve.RunAll(context.Background(),
					serve.Server{Handler: ok(), Config: serve.Config{Listener: ln0}},
					serve.Server{Handler: ok(), Config: serve.Config{Addr: ln1.Addr().String()}},
				)
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			lns := make([]net.Listener, 2)
			for i := range lns {
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer ln.Close() //nolint:errcheck // Already closed on most paths.
				lns[i] = ln
			}

			_ = tt.run(lns[0], lns[1])

			for _, i := range tt.handed {
				if _, err := lns[i].Accept(); err == nil {
					t.Errorf("listener %d is still accepting after RunAll returned", i)
				}
			}
		})
	}
}

func TestRunAllReturnsNilWhenTheContextIsAlreadyDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var ran atomic.Bool
	cfg := serve.Config{
		Addr:     "127.0.0.1:0",
		OnListen: func(net.Addr) { ran.Store(true) },
		OnDrain:  func() { ran.Store(true) },
	}
	err := serve.RunAll(ctx, serve.Server{Handler: ok(), Config: cfg}, serve.Server{Handler: ok(), Config: cfg})
	if err != nil {
		t.Fatalf("err = %v, want nil for a context that is already done", err)
	}
	if ran.Load() {
		t.Error("a server listened or drained for a context that is already done")
	}
}

func TestRunAllStopsTheServersInOrder(t *testing.T) {
	b := newBlocker(t)
	draining0 := make(chan struct{})
	var drained1, sawFinished atomic.Bool
	g := startAll(t,
		serve.Server{Handler: b, Config: serve.Config{OnDrain: func() { close(draining0) }}},
		serve.Server{Handler: ok(), Config: serve.Config{OnDrain: func() {
			sawFinished.Store(b.finished.Load())
			drained1.Store(true)
		}}},
	)

	held := call(client(t), g.url(0, "/"))
	b.running(t)

	g.cancel()
	select {
	case <-draining0:
	case <-time.After(wait):
		t.Fatal("server 0 never started to drain")
	}

	res := await(t, call(client(t), g.url(1, "/")))
	if res.err != nil || res.status != http.StatusOK {
		t.Fatalf("server 1 answered %d, %v during the drain of server 0, want 200", res.status, res.err)
	}
	if drained1.Load() {
		t.Fatal("server 1 started to drain before server 0 returned")
	}

	b.free()
	if err := g.wait(t); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !sawFinished.Load() {
		t.Error("server 1 started to drain before the request of server 0 finished")
	}
	if res := await(t, held); res.err != nil || res.body != "late" {
		t.Errorf("held request = %q, %v, want the answer the drain waited for", res.body, res.err)
	}
}

func TestRunAllKeepsTheOrderWhenAMiddleServerFails(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	boom := errors.New("listener failed")
	fail := make(chan struct{})

	b := newBlocker(t)
	draining0 := make(chan struct{})
	var drained2, sawFinished atomic.Bool
	g := startAll(t,
		serve.Server{Handler: b, Config: serve.Config{OnDrain: func() { close(draining0) }}},
		serve.Server{Handler: ok(), Config: serve.Config{
			Listener: &failAfterAcceptListener{Listener: ln, fail: fail, err: boom},
		}},
		serve.Server{Handler: ok(), Config: serve.Config{OnDrain: func() {
			sawFinished.Store(b.finished.Load())
			drained2.Store(true)
		}}},
	)

	held := call(client(t), g.url(0, "/"))
	b.running(t)
	// Server 1 serves its first connection, then fails on the next Accept.
	if res := await(t, call(client(t), g.url(1, "/"))); res.err != nil {
		t.Fatalf("request to server 1: %v", res.err)
	}

	close(fail)
	select {
	case <-draining0:
	case <-time.After(wait):
		t.Fatal("the failure of server 1 did not stop server 0")
	}

	res := await(t, call(client(t), g.url(2, "/")))
	if res.err != nil || res.status != http.StatusOK {
		t.Fatalf("server 2 answered %d, %v during the drain of server 0, want 200", res.status, res.err)
	}
	if drained2.Load() {
		t.Fatal("server 2 started to drain before server 0 returned")
	}

	b.free()
	err = g.wait(t)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the failure of server 1", err)
	}
	if !strings.HasPrefix(err.Error(), "serve: server 1: ") {
		t.Errorf("err = %q, want it to name server 1", err)
	}
	if !sawFinished.Load() {
		t.Error("server 2 started to drain before the request of server 0 finished")
	}
	if res := await(t, held); res.err != nil || res.body != "late" {
		t.Errorf("held request = %q, %v, want the answer the drain waited for", res.body, res.err)
	}
}

func TestRunAllStopsEveryServerWhenOneFails(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveErr := errors.New("accept failed")
	var drains atomic.Int32
	g := startAll(t,
		serve.Server{Handler: ok(), Config: serve.Config{OnDrain: func() { drains.Add(1) }}},
		serve.Server{Handler: ok(), Config: serve.Config{
			Listener: acceptErrorListener{Listener: ln, err: serveErr},
		}},
	)

	err = g.wait(t)
	if !errors.Is(err, serveErr) {
		t.Fatalf("err = %v, want the serving failure of server 1", err)
	}
	if !strings.HasPrefix(err.Error(), "serve: server 1: ") {
		t.Errorf("err = %q, want it to name server 1", err)
	}
	if n := drains.Load(); n != 1 {
		t.Errorf("server 0 ran OnDrain %d times, want once", n)
	}
}

func TestRunAllReportsEveryFailure(t *testing.T) {
	boom := []error{errors.New("boom 0"), errors.New("boom 1")}
	servers := make([]serve.Server, len(boom))
	for i := range servers {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		servers[i] = serve.Server{Handler: ok(), Config: serve.Config{
			Listener: errListener{Listener: ln, err: boom[i]},
		}}
	}
	g := startAll(t, servers...)

	for i := range servers {
		if res := await(t, call(client(t), g.url(i, "/"))); res.err != nil {
			t.Fatalf("request to server %d: %v", i, res.err)
		}
	}

	err := g.stop(t)
	for i, want := range boom {
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want the failure of server %d", err, i)
		}
	}
	for i, line := range strings.Split(err.Error(), "\n") {
		if prefix := fmt.Sprintf("serve: server %d: ", i); !strings.HasPrefix(line, prefix) || !strings.Contains(line, boom[i].Error()) {
			t.Errorf("line %d = %q, want %q naming its own server", i, line, prefix)
		}
	}
}

func TestRunAllClosesItsListenersWhenOnListenPanics(t *testing.T) {
	var addrs []net.Addr
	onListen := func(a net.Addr) { addrs = append(addrs, a) }
	func() {
		defer func() { _ = recover() }()
		_ = serve.RunAll(context.Background(),
			serve.Server{Handler: ok(), Config: serve.Config{Addr: "127.0.0.1:0", OnListen: onListen}},
			serve.Server{Handler: ok(), Config: serve.Config{
				Addr: "127.0.0.1:0",
				OnListen: func(a net.Addr) {
					onListen(a)
					panic("boom")
				},
			}},
		)
	}()

	if len(addrs) != 2 {
		t.Fatalf("OnListen ran %d times, want 2", len(addrs))
	}
	for i, a := range addrs {
		if conn, err := net.Dial("tcp", a.String()); err == nil {
			conn.Close() //nolint:errcheck // The test fails either way.
			t.Errorf("server %d is still accepting after OnListen panicked", i)
		}
	}
}

func TestRunAllChecksTheServersForAContextAlreadyDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var built, listened atomic.Bool
	err := serve.RunAll(ctx,
		serve.Server{Handler: ok(), Config: serve.Config{
			Addr:     "127.0.0.1:0",
			OnListen: func(net.Addr) { listened.Store(true) },
			OnServer: func(*http.Server) error {
				built.Store(true)
				return nil
			},
		}},
		serve.Server{Handler: ok(), Config: serve.Config{
			Addr:      "127.0.0.1:0",
			TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13},
		}},
	)
	if err == nil || !strings.HasPrefix(err.Error(), "serve: server 1: ") {
		t.Fatalf("err = %v, want the TLS check of server 1", err)
	}
	if !built.Load() {
		t.Error("OnServer did not run for a context that is already done")
	}
	if listened.Load() {
		t.Error("RunAll bound an address for a context that is already done")
	}
}
