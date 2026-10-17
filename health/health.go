// Package health answers the liveness and readiness probes of an orchestrator
// or a load balancer.
//
// Liveness says that the process runs, and it answers 200 for as long as the
// router answers at all. Readiness says that the process can take traffic: it
// runs every [Check] on each probe, at once and each under its own timeout,
// and answers 503 when a critical one fails. A check marked NonCritical that
// fails leaves the answer at 200, with the status "degraded".
//
// Readiness also answers 503 once the server begins to stop, as reported by
// serve.Stopping, so a load balancer takes the server out of rotation during
// serve.Config.DrainDelay and no flag or gate of your own is needed. It runs
// no check then.
//
// Mount the probes on the router of the application:
//
//	health.Mount(r, health.Config{
//		Checks: []health.Check{
//			{Name: "postgres", Func: db.PingContext},
//			{Name: "cache", Func: cache.Ping, NonCritical: true},
//		},
//	})
//
// or serve [Handler] from a private server beside the public one, through
// serve.RunAll.
//
// The answer is a JSON [Report]. It names each check and whether it passed. It
// leaves out the error a check reported, which may name a host or a user,
// unless Config.ExposeErrors is set.
package health

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/serve"
)

// DefaultTimeout bounds a check when neither Check.Timeout nor Config.Timeout
// sets a bound.
const DefaultTimeout = 2 * time.Second

// The paths of the probes when Config leaves them empty.
const (
	DefaultLivePath  = "/livez"
	DefaultReadyPath = "/readyz"
)

// The values of Report.Status.
const (
	StatusOK          = "ok"
	StatusDegraded    = "degraded"
	StatusUnavailable = "unavailable"
	StatusStopping    = "stopping"
)

// Check is one dependency that readiness asks about. Func reports nil when
// the dependency is fine; a method such as (*sql.DB).PingContext fits as it
// stands. Timeout bounds one call, and zero takes Config.Timeout. A check is
// critical unless NonCritical is set: a critical failure makes readiness
// answer 503, and any other failure makes it answer 200 as "degraded".
type Check struct {
	Func        func(ctx context.Context) error
	Name        string
	Timeout     time.Duration
	NonCritical bool
}

// Config configures [Mount] and [Handler]. LivePath and ReadyPath are the
// paths of the probes, and empty ones take [DefaultLivePath] and
// [DefaultReadyPath]. Timeout bounds each check that sets no Timeout of its
// own, and zero takes [DefaultTimeout]. ExposeErrors puts the error of a
// failed check in the [Report].
type Config struct {
	LivePath     string
	ReadyPath    string
	Checks       []Check
	Timeout      time.Duration
	ExposeErrors bool
}

// Report is the JSON body of both probes. Checks lists the result of every
// check in the order of Config.Checks, and is empty for liveness and for a
// server that is stopping.
type Report struct {
	Status string   `json:"status"`
	Checks []Result `json:"checks,omitempty"`
}

// Result is the outcome of one check. Error is the error the check reported,
// and it is empty unless Config.ExposeErrors is set. A check that ran out of
// time reports [context.DeadlineExceeded], and one that panicked reports the
// value it panicked with.
type Result struct {
	Name     string `json:"name"`
	Error    string `json:"error,omitempty"`
	OK       bool   `json:"ok"`
	Critical bool   `json:"critical"`
}

// probes is a Config checked and with its defaults applied.
type probes struct {
	live, ready string
	checks      []Check
	expose      bool
}

func compile(fn string, cfg Config) probes {
	p := probes{
		live:   path(fn, "LivePath", cfg.LivePath, DefaultLivePath),
		ready:  path(fn, "ReadyPath", cfg.ReadyPath, DefaultReadyPath),
		checks: make([]Check, len(cfg.Checks)),
		expose: cfg.ExposeErrors,
	}
	if p.live == p.ready {
		panic("health: " + fn + " got the same path for LivePath and ReadyPath: " + p.live)
	}
	if cfg.Timeout < 0 {
		panic("health: " + fn + " needs a Timeout of zero or more")
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	seen := make(map[string]bool, len(cfg.Checks))
	for i, ch := range cfg.Checks {
		switch {
		case ch.Name == "":
			panic(fmt.Sprintf("health: %s got a check without a Name at index %d", fn, i))
		case seen[ch.Name]:
			panic(fmt.Sprintf("health: %s got the check %q twice", fn, ch.Name))
		case ch.Func == nil:
			panic(fmt.Sprintf("health: %s got the check %q without a Func", fn, ch.Name))
		case ch.Timeout < 0:
			panic(fmt.Sprintf("health: %s got the check %q with a negative Timeout", fn, ch.Name))
		}
		seen[ch.Name] = true
		if ch.Timeout == 0 {
			ch.Timeout = timeout
		}
		p.checks[i] = ch
	}
	return p
}

func path(fn, field, v, fallback string) string {
	if v == "" {
		return fallback
	}
	if !strings.HasPrefix(v, "/") {
		panic("health: " + fn + " needs a " + field + " that starts with a slash, as in \"" + fallback + "\"")
	}
	return v
}

// Mount registers the liveness and the readiness probe on r, for GET, which
// also answers HEAD. Mount them on a scope with no auth and no rate limit, as
// a probe sends no credentials and comes often.
//
// Mount panics if r is nil, a path does not start with a slash, both paths are
// the same, a timeout is negative, or a check has no Name, no Func or the Name
// of another check.
func Mount[C router.Context](r *router.Router[C], cfg Config) {
	if r == nil {
		panic("health: Mount needs a router")
	}
	p := compile("Mount", cfg)
	r.GET(p.live, func(c C) error { return answer(c, http.StatusOK, Report{Status: StatusOK}) })
	r.GET(p.ready, func(c C) error {
		status, rep := p.evaluate(c.Request().Context())
		return answer(c, status, rep)
	})
}

// Handler returns an [http.Handler] that serves the probes of cfg and answers
// 404 for any other path, for a private server beside the public one:
//
//	serve.RunAll(ctx,
//		serve.Server{Handler: app, Config: serve.Config{Addr: ":8080", DrainDelay: 5 * time.Second}},
//		serve.Server{Handler: health.Handler(cfg), Config: serve.Config{Addr: ":9090"}},
//	)
//
// serve.Stopping closes for every server of RunAll at once, so readiness on the
// private server answers 503 as soon as the public one begins to stop.
//
// Handler panics on the Config that [Mount] refuses.
func Handler(cfg Config) http.Handler {
	compile("Handler", cfg)
	r := router.New(func(http.ResponseWriter, *http.Request) *healthContext { return new(healthContext) })
	Mount(r, cfg)
	return r
}

type healthContext struct{ router.Base }

func answer(c router.Context, status int, rep Report) error {
	b, _ := router.FromContext(c)
	b.SetHeader(router.HeaderCacheControl, "no-store")
	return b.JSON(status, rep)
}

func (p probes) evaluate(ctx context.Context) (int, Report) {
	select {
	case <-serve.Stopping(ctx):
		return http.StatusServiceUnavailable, Report{Status: StatusStopping}
	default:
	}

	results := make([]Result, len(p.checks))
	var wg sync.WaitGroup
	for i, ch := range p.checks {
		wg.Go(func() {
			err := run(ctx, ch)
			results[i] = Result{Name: ch.Name, OK: err == nil, Critical: !ch.NonCritical}
			if err != nil && p.expose {
				results[i].Error = err.Error()
			}
		})
	}
	wg.Wait()

	rep := Report{Status: StatusOK, Checks: results}
	for _, r := range results {
		switch {
		case r.OK:
		case r.Critical:
			rep.Status = StatusUnavailable
			return http.StatusServiceUnavailable, rep
		default:
			rep.Status = StatusDegraded
		}
	}
	return http.StatusOK, rep
}

// run calls the check under its timeout. A check that ignores its context
// still answers on time: run stops waiting at the deadline, and the call goes
// on alone until it returns. A panic of the check becomes its error.
func run(ctx context.Context, ch Check) error {
	ctx, cancel := context.WithTimeout(ctx, ch.Timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if v := recover(); v != nil {
				done <- fmt.Errorf("health: the check panicked: %v", v)
			}
		}()
		done <- ch.Func(ctx)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
