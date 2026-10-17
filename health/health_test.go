package health_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/health"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/serve"
)

type appContext struct{ router.Base }

func newRouter(cfg health.Config) *router.Router[*appContext] {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	health.Mount(r, cfg)
	return r
}

func pass(context.Context) error { return nil }

func fail(context.Context) error { return errors.New("dial tcp 10.0.0.5:5432: connection refused") }

func report(t *testing.T, res *routertest.Response) health.Report {
	t.Helper()
	rep, err := res.JSON[health.Report]()
	if err != nil {
		t.Fatalf("the body %q is not a report: %v", res.Body, err)
	}
	return rep
}

func mustPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Errorf("did not panic, want a panic containing %q", want)
			return
		}
		if msg, _ := r.(string); !strings.Contains(msg, want) {
			t.Errorf("panic = %v, want it to contain %q", r, want)
		}
	}()
	f()
}

func TestLivenessAnswersOK(t *testing.T) {
	r := newRouter(health.Config{Checks: []health.Check{{Name: "db", Func: fail}}})
	routertest.Get(r, "/livez").Expect(t).
		Status(http.StatusOK).
		ContentType(router.MIMEApplicationJSON).
		Header(router.HeaderCacheControl, "no-store").
		JSON(map[string]any{"status": "ok"})
	routertest.Do(r, http.MethodHead, "/livez").Expect(t).Status(http.StatusOK).Body("")
}

func TestReadiness(t *testing.T) {
	tests := []struct {
		name   string
		checks []health.Check
		status int
		want   string
	}{
		{"no check", nil, http.StatusOK, health.StatusOK},
		{"every check passes", []health.Check{{Name: "db", Func: pass}, {Name: "cache", Func: pass}}, http.StatusOK, health.StatusOK},
		{"a critical check fails", []health.Check{{Name: "db", Func: fail}, {Name: "cache", Func: pass}}, http.StatusServiceUnavailable, health.StatusUnavailable},
		{"a non-critical check fails", []health.Check{{Name: "db", Func: pass}, {Name: "cache", Func: fail, NonCritical: true}}, http.StatusOK, health.StatusDegraded},
		{"both kinds fail", []health.Check{{Name: "cache", Func: fail, NonCritical: true}, {Name: "db", Func: fail}}, http.StatusServiceUnavailable, health.StatusUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := routertest.Get(newRouter(health.Config{Checks: tt.checks}), "/readyz")
			res.Expect(t).Status(tt.status).Header(router.HeaderCacheControl, "no-store")
			rep := report(t, res)
			if rep.Status != tt.want {
				t.Errorf("status = %q, want %q", rep.Status, tt.want)
			}
			if len(rep.Checks) != len(tt.checks) {
				t.Fatalf("checks = %+v, want one result for each of %d", rep.Checks, len(tt.checks))
			}
			for i, ch := range tt.checks {
				got := rep.Checks[i]
				if got.Name != ch.Name || got.Critical == ch.NonCritical || got.Error != "" {
					t.Errorf("check %d = %+v, want %q in order, critical %v, no error", i, got, ch.Name, !ch.NonCritical)
				}
			}
		})
	}
}

func TestReadinessHidesTheErrorUnlessExposed(t *testing.T) {
	checks := []health.Check{{Name: "db", Func: fail}}
	hidden := routertest.Get(newRouter(health.Config{Checks: checks}), "/readyz")
	hidden.Expect(t).NotContains("10.0.0.5")

	exposed := report(t, routertest.Get(newRouter(health.Config{Checks: checks, ExposeErrors: true}), "/readyz"))
	if !strings.Contains(exposed.Checks[0].Error, "connection refused") {
		t.Errorf("error = %q, want the error of the check", exposed.Checks[0].Error)
	}
}

func TestReadinessBoundsACheckThatIgnoresItsContext(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stuck := func(context.Context) error {
		<-release
		return nil
	}
	r := newRouter(health.Config{
		Timeout:      time.Hour,
		ExposeErrors: true,
		Checks:       []health.Check{{Name: "stuck", Func: stuck, Timeout: 20 * time.Millisecond}},
	})

	start := time.Now()
	res := routertest.Get(r, "/readyz")
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the probe took %s, want the 20ms of the check", took)
	}
	res.Expect(t).Status(http.StatusServiceUnavailable)
	if got := report(t, res).Checks[0].Error; got != context.DeadlineExceeded.Error() {
		t.Errorf("error = %q, want the deadline", got)
	}
}

func TestReadinessTakesTheTimeoutOfTheConfig(t *testing.T) {
	var deadline time.Duration
	r := newRouter(health.Config{
		Timeout: 3 * time.Second,
		Checks: []health.Check{{Name: "db", Func: func(ctx context.Context) error {
			d, _ := ctx.Deadline()
			deadline = time.Until(d)
			return nil
		}}},
	})
	routertest.Get(r, "/readyz").Expect(t).Status(http.StatusOK)
	if deadline <= 2*time.Second || deadline > 3*time.Second {
		t.Errorf("the check had %s left, want the 3s of the config", deadline)
	}
}

func TestReadinessTakesTheDefaultTimeout(t *testing.T) {
	var deadline time.Duration
	r := newRouter(health.Config{Checks: []health.Check{{Name: "db", Func: func(ctx context.Context) error {
		d, _ := ctx.Deadline()
		deadline = time.Until(d)
		return nil
	}}}})
	routertest.Get(r, "/readyz").Expect(t).Status(http.StatusOK)
	if deadline <= health.DefaultTimeout-time.Second || deadline > health.DefaultTimeout {
		t.Errorf("the check had %s left, want DefaultTimeout", deadline)
	}
}

func TestReadinessRunsTheChecksAtOnce(t *testing.T) {
	var started atomic.Int32
	both := make(chan struct{})
	meet := func(ctx context.Context) error {
		if started.Add(1) == 2 {
			close(both)
		}
		select {
		case <-both:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r := newRouter(health.Config{
		Timeout: time.Second,
		Checks:  []health.Check{{Name: "a", Func: meet}, {Name: "b", Func: meet}},
	})
	routertest.Get(r, "/readyz").Expect(t).Status(http.StatusOK)
}

func TestReadinessContainsAPanic(t *testing.T) {
	r := newRouter(health.Config{
		ExposeErrors: true,
		Checks:       []health.Check{{Name: "bad", Func: func(context.Context) error { panic("boom") }}},
	})
	res := routertest.Get(r, "/readyz")
	res.Expect(t).Status(http.StatusServiceUnavailable)
	if got := report(t, res).Checks[0].Error; !strings.Contains(got, "panicked: boom") {
		t.Errorf("error = %q, want the panic", got)
	}
}

func TestTheProbesTakeTheirPaths(t *testing.T) {
	r := newRouter(health.Config{LivePath: "/health/live", ReadyPath: "/health/ready"})
	routertest.Get(r, "/health/live").Expect(t).Status(http.StatusOK)
	routertest.Get(r, "/health/ready").Expect(t).Status(http.StatusOK)
	routertest.Get(r, "/livez").Expect(t).Status(http.StatusNotFound)
}

func TestHandlerServesTheProbesAlone(t *testing.T) {
	h := health.Handler(health.Config{Checks: []health.Check{{Name: "db", Func: fail}}})
	routertest.Get(h, "/livez").Expect(t).Status(http.StatusOK)
	routertest.Get(h, "/readyz").Expect(t).Status(http.StatusServiceUnavailable)
	routertest.Get(h, "/metrics").Expect(t).Status(http.StatusNotFound)
	routertest.Do(h, http.MethodPost, "/readyz").Expect(t).Status(http.StatusMethodNotAllowed)
}

func TestReadinessAnswers503OnceTheServerStops(t *testing.T) {
	var ran atomic.Int32
	h := health.Handler(health.Config{Checks: []health.Check{{Name: "db", Func: func(context.Context) error {
		ran.Add(1)
		return nil
	}}}})

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	addr := make(chan string, 1)
	onDrain := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- serve.Run(ctx, h, serve.Config{
			Addr:       "127.0.0.1:0",
			DrainDelay: time.Second,
			Logger:     slog.New(slog.DiscardHandler),
			OnListen:   func(a net.Addr) { addr <- "http://" + a.String() },
			OnDrain:    func() { close(onDrain) },
		})
	}()
	base := <-addr
	cl := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
	get := func(path string) (int, string) {
		t.Helper()
		res, err := cl.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close() //nolint:errcheck // The test is done with it.
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, string(body)
	}

	if code, _ := get("/readyz"); code != http.StatusOK {
		t.Fatalf("before the stop: %d, want 200", code)
	}
	stop()
	<-onDrain
	code, body := get("/readyz")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, `"status":"stopping"`) {
		t.Fatalf("during the delay: %d %s, want 503 stopping", code, body)
	}
	if code, _ := get("/livez"); code != http.StatusOK {
		t.Fatalf("liveness during the delay: %d, want 200", code)
	}
	if n := ran.Load(); n != 1 {
		t.Errorf("the check ran %d times, want once: a stopping server runs none", n)
	}
	if err := <-errc; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

func TestMountRefusesABadConfig(t *testing.T) {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	tests := []struct {
		name string
		cfg  health.Config
		want string
	}{
		{"relative live path", health.Config{LivePath: "livez"}, "needs a LivePath that starts with a slash"},
		{"relative ready path", health.Config{ReadyPath: "readyz"}, "needs a ReadyPath that starts with a slash"},
		{"one path twice", health.Config{LivePath: "/h", ReadyPath: "/h"}, "got the same path for LivePath and ReadyPath"},
		{"negative timeout", health.Config{Timeout: -1}, "needs a Timeout of zero or more"},
		{"no name", health.Config{Checks: []health.Check{{Func: pass}}}, "got a check without a Name at index 0"},
		{"twice", health.Config{Checks: []health.Check{{Name: "db", Func: pass}, {Name: "db", Func: pass}}}, `got the check "db" twice`},
		{"no func", health.Config{Checks: []health.Check{{Name: "db"}}}, `got the check "db" without a Func`},
		{"negative check timeout", health.Config{Checks: []health.Check{{Name: "db", Func: pass, Timeout: -1}}}, `got the check "db" with a negative Timeout`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustPanic(t, "health: Mount "+tt.want, func() { health.Mount(r, tt.cfg) })
			mustPanic(t, "health: Handler "+tt.want, func() { health.Handler(tt.cfg) })
		})
	}
	mustPanic(t, "health: Mount needs a router", func() { health.Mount[*appContext](nil, health.Config{}) })
}
