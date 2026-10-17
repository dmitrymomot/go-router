package middleware

import (
	"errors"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
)

type shedContext struct{ router.Base }

// fixed is a criterion of constant pressure.
func fixed(p float64) func() float64 { return func() float64 { return p } }

func always(v float64) func() float64 { return func() float64 { return v } }

func TestLoadShedRamp(t *testing.T) {
	newShedderAt := func(p float64, roll float64) *shedder {
		s := newShedder(0, 0, 0, 0.5, 0.1, fixed(p))
		s.rand = always(roll)
		return s
	}
	tests := []struct {
		name           string
		pressure, roll float64
		admit          bool
	}{
		// Below the threshold every request passes, whatever the roll.
		{"below the threshold", 0.4, 0, true},
		{"at the threshold", 0.5, 0, true},
		// At saturation the share turned away is 1 - floor = 0.9.
		{"saturated, a low roll", 1, 0, false},
		{"saturated, a roll under the share", 1, 0.89, false},
		{"saturated, a roll past the share", 1, 0.9, true},
		// Halfway up the ramp the share is 0.5 * 0.9 = 0.45.
		{"halfway, a roll under the share", 0.75, 0.4, false},
		{"halfway, a roll past the share", 0.75, 0.5, true},
		// A pressure past 1 reads as 1.
		{"past saturation", 7, 0.89, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := newShedderAt(tt.pressure, tt.roll).admit(); ok != tt.admit {
				t.Errorf("admit = %v, want %v", ok, tt.admit)
			}
		})
	}
}

func TestLoadShedDefaults(t *testing.T) {
	s := newShedder(10, 0, 0, 0, 0, nil)
	if s.alpha != DefaultLoadShedAlpha || s.threshold != DefaultLoadShedThreshold || s.floor != DefaultLoadShedFloor {
		t.Errorf("alpha, threshold, floor = %v, %v, %v; want the defaults", s.alpha, s.threshold, s.floor)
	}
}

func TestLoadShedLatencyRisesAndClamps(t *testing.T) {
	s := newShedder(0, 100*time.Millisecond, 0, 0, 0, nil)
	if p := s.load(); p != 0 {
		t.Fatalf("pressure with no sample = %v, want 0", p)
	}
	// The first sample sets the average: 50ms of 100ms.
	s.observe(50 * time.Millisecond)
	if p := s.load(); math.Abs(p-0.5) > 1e-9 {
		t.Fatalf("pressure = %v, want 0.5", p)
	}
	// 0.2*500 + 0.8*50 = 140ms, past the target, which reads as 1.
	s.observe(500 * time.Millisecond)
	if p := s.load(); p != 1 {
		t.Fatalf("pressure = %v, want 1", p)
	}
	if ewma := math.Float64frombits(s.ewma.Load()); math.Abs(ewma-float64(140*time.Millisecond)) > 1 {
		t.Fatalf("average = %v, want 140ms", time.Duration(ewma))
	}
}

func TestLoadShedObservesConcurrently(t *testing.T) {
	s := newShedder(0, time.Second, 0, 0, 0, nil)
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() { s.observe(10 * time.Millisecond) })
	}
	wg.Wait()
	if ewma := math.Float64frombits(s.ewma.Load()); math.Abs(ewma-float64(10*time.Millisecond)) > 1 {
		t.Fatalf("average = %v, want 10ms", time.Duration(ewma))
	}
}

func TestLoadShedReadsTheHighestCriterion(t *testing.T) {
	s := newShedder(4, 100*time.Millisecond, 0, 0, 0, fixed(0.3))
	s.inflight.Store(2)
	s.observe(20 * time.Millisecond)
	if p := s.load(); p != 0.5 {
		t.Fatalf("pressure = %v, want the 0.5 of the requests in flight", p)
	}
}

func TestLoadShedGaugeFailsOpen(t *testing.T) {
	for name, f := range map[string]func() float64{
		"panic":    func() float64 { panic("boom") },
		"NaN":      fixed(math.NaN()),
		"negative": fixed(-1),
	} {
		t.Run(name, func(t *testing.T) {
			s := newShedder(0, 0, 0, 0, 0, f)
			s.rand = always(0)
			if p, ok := s.admit(); !ok || p != 0 {
				t.Errorf("admit = %v, %v; want a pressure of 0 that lets the request through", p, ok)
			}
		})
	}
}

func shedRouter(cfg LoadShedConfig[*shedContext], s *shedder, h router.HandlerFunc[*shedContext]) *router.Router[*shedContext] {
	r := router.New(func(http.ResponseWriter, *http.Request) *shedContext { return new(shedContext) })
	r.Use(loadShed(cfg, s))
	r.GET("/", h)
	r.GET("/health", h)
	return r
}

func TestLoadShedRefusesWithA503(t *testing.T) {
	s := newShedder(0, 0, 0, 0, 0, fixed(1))
	s.rand = always(0)
	var got error
	cfg := LoadShedConfig[*shedContext]{
		RetryAfter: 1500 * time.Millisecond,
		Skip:       func(c *shedContext) bool { return c.Path() == "/health" },
		OnReject: func(_ *shedContext, err error) error {
			got = err
			return err
		},
	}
	r := shedRouter(cfg, s, func(c *shedContext) error { return c.String(http.StatusOK, "ok") })

	routertest.Get(r, "/").Expect(t).Status(http.StatusServiceUnavailable).Header(router.HeaderRetryAfter, "2")
	if !errors.Is(got, router.ErrServiceUnavailable) || !strings.Contains(got.Error(), "pressure of 1.00") {
		t.Errorf("err = %v, want a 503 that names the pressure", got)
	}
	routertest.Get(r, "/health").Expect(t).Status(http.StatusOK)
}

func TestLoadShedRetryAfterDefault(t *testing.T) {
	s := newShedder(0, 0, 0, 0, 0, fixed(1))
	s.rand = always(0)
	r := shedRouter(LoadShedConfig[*shedContext]{}, s, func(c *shedContext) error { return nil })
	routertest.Get(r, "/").Expect(t).Status(http.StatusServiceUnavailable).Header(router.HeaderRetryAfter, "1")
}

func TestLoadShedCountsAndTimesTheRequests(t *testing.T) {
	s := newShedder(2, time.Second, 0, 0, 0, nil)
	now := time.Unix(0, 0)
	s.now = func() time.Time { return now }
	var during int64
	r := shedRouter(LoadShedConfig[*shedContext]{}, s, func(c *shedContext) error {
		during = s.inflight.Load()
		now = now.Add(250 * time.Millisecond)
		return router.ErrConflict
	})

	routertest.Get(r, "/").Expect(t).Status(http.StatusConflict)
	if during != 1 || s.inflight.Load() != 0 {
		t.Errorf("in flight = %d during and %d after, want 1 and 0", during, s.inflight.Load())
	}
	if ewma := time.Duration(math.Float64frombits(s.ewma.Load())); ewma != 250*time.Millisecond {
		t.Errorf("average = %v, want the 250ms of the failed request too", ewma)
	}
}

func TestLoadShedTurnsAwayAtSaturation(t *testing.T) {
	s := newShedder(1, 0, 0, 0, 0, nil)
	s.rand = always(0)
	release, entered := make(chan struct{}), make(chan struct{})
	r := shedRouter(LoadShedConfig[*shedContext]{}, s, func(c *shedContext) error {
		close(entered)
		<-release
		return nil
	})

	first := make(chan int, 1)
	go func() { first <- routertest.Get(r, "/").StatusCode }()
	<-entered
	routertest.Get(r, "/").Expect(t).Status(http.StatusServiceUnavailable)
	close(release)
	if code := <-first; code != http.StatusOK {
		t.Errorf("the first request = %d, want 200", code)
	}
}

func TestLoadShedRefusesABadConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  LoadShedConfig[*shedContext]
		want string
	}{
		{"no criterion", LoadShedConfig[*shedContext]{}, "needs a criterion"},
		{"negative in flight", LoadShedConfig[*shedContext]{MaxInFlight: -1}, "MaxInFlight and a TargetLatency of zero or more"},
		{"negative latency", LoadShedConfig[*shedContext]{MaxInFlight: 1, TargetLatency: -1}, "MaxInFlight and a TargetLatency of zero or more"},
		{"alpha past 1", LoadShedConfig[*shedContext]{MaxInFlight: 1, Alpha: 1.5}, "Alpha is 1.5"},
		{"NaN alpha", LoadShedConfig[*shedContext]{MaxInFlight: 1, Alpha: math.NaN()}, "Alpha is NaN"},
		{"threshold of 1", LoadShedConfig[*shedContext]{MaxInFlight: 1, Threshold: 1}, "Threshold is 1"},
		{"negative floor", LoadShedConfig[*shedContext]{MaxInFlight: 1, Floor: -0.1}, "Floor is -0.1"},
		{"negative retry", LoadShedConfig[*shedContext]{MaxInFlight: 1, RetryAfter: -1}, "RetryAfter of zero or more"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				msg, _ := recover().(string)
				if !strings.Contains(msg, tt.want) {
					t.Errorf("panic = %q, want it to contain %q", msg, tt.want)
				}
			}()
			LoadShedWithConfig(tt.cfg)
		})
	}
	defer func() {
		if msg, _ := recover().(string); !strings.Contains(msg, "needs a criterion") {
			t.Errorf("panic = %q, want the missing criterion", msg)
		}
	}()
	LoadShed[*shedContext](0, 0)
}
