package middleware

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/dmitrymomot/go-router"
)

// The defaults of [LoadShedWithConfig].
const (
	DefaultLoadShedThreshold  = 0.8
	DefaultLoadShedFloor      = 0.05
	DefaultLoadShedAlpha      = 0.2
	DefaultLoadShedRetryAfter = time.Second
)

// LoadShedConfig configures [LoadShedWithConfig].
//
// The middleware measures the pressure on the server as a number from 0, idle,
// to 1, saturated: the highest of the criteria it has. MaxInFlight is the
// count of requests in flight that reads as saturated. TargetLatency is the
// latency that reads as saturated, held as an average that weighs each new
// request by Alpha, from 0 to 1, where zero takes [DefaultLoadShedAlpha].
// Pressure is a criterion of your own, such as the load of the CPU or the
// depth of a queue; it runs on every request, and a panic in it counts as 0,
// so a broken gauge never turns traffic away. At least one criterion is
// required, and a zero one is off.
//
// Below Threshold every request passes, and zero takes
// [DefaultLoadShedThreshold]. Above it the middleware turns away a share of
// the requests that grows with the pressure, up to all but Floor of them at
// saturation, and zero takes [DefaultLoadShedFloor]. The floor cannot be zero:
// the requests it lets through are what the latency criterion learns from, so
// a server that turned away every request would read as saturated for good.
//
// A request turned away gets [router.ErrServiceUnavailable] and a Retry-After
// of RetryAfter, rounded up to whole seconds, where zero takes
// [DefaultLoadShedRetryAfter]. OnReject answers it: the Retry-After header is
// already set when it runs, and err is the 503 the middleware would report.
// Return an error to report it in place of the request, or nil to say that
// OnReject wrote the answer itself; either way the request goes no further. A
// nil OnReject reports err.
type LoadShedConfig[C router.Context] struct {
	Skip          func(c C) bool
	OnReject      func(c C, err error) error
	Pressure      func() float64
	MaxInFlight   int
	TargetLatency time.Duration
	Alpha         float64
	Threshold     float64
	Floor         float64
	RetryAfter    time.Duration
}

// LoadShed turns away a growing share of requests with a 503 once the server
// is overloaded, so the requests it lets through still succeed: when more
// than 80% of maxInFlight requests are in flight, or the average latency
// passes 80% of targetLatency. A zero argument turns its criterion off.
//
// Unlike [Throttle], which refuses every request past a fixed count, LoadShed
// refuses at random, more of them the closer the server is to saturation, and
// it reads the latency too. Unlike [RateLimit] it protects the server rather
// than share it fairly among clients. The counts live in one process.
//
// See Order in the package doc for where it goes.
//
// LoadShed panics when both arguments are zero, or either is negative.
func LoadShed[C router.Context](maxInFlight int, targetLatency time.Duration) router.Middleware[C] {
	return LoadShedWithConfig(LoadShedConfig[C]{MaxInFlight: maxInFlight, TargetLatency: targetLatency})
}

// LoadShedWithConfig is [LoadShed] with a configuration.
//
// LoadShedWithConfig panics when no criterion is set, on a negative
// MaxInFlight, TargetLatency or RetryAfter, on an Alpha outside 0 to 1, and on
// a Threshold or a Floor outside 0 to 1, 1 itself excluded.
func LoadShedWithConfig[C router.Context](cfg LoadShedConfig[C]) router.Middleware[C] {
	s := newShedder(cfg.MaxInFlight, cfg.TargetLatency, cfg.Alpha, cfg.Threshold, cfg.Floor, cfg.Pressure)
	if cfg.RetryAfter < 0 {
		panic("middleware: LoadShedConfig needs a RetryAfter of zero or more")
	}
	return loadShed(cfg, s)
}

// loadShed is the middleware of LoadShedWithConfig over s, which a test of the
// package builds with a clock and a random source of its own.
func loadShed[C router.Context](cfg LoadShedConfig[C], s *shedder) router.Middleware[C] {
	retryAfter := cfg.RetryAfter
	if retryAfter == 0 {
		retryAfter = DefaultLoadShedRetryAfter
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}
			if p, ok := s.admit(); !ok {
				setRetryAfter(c.Response(), retryAfter)
				err := router.ErrServiceUnavailable.WithError(
					fmt.Errorf("middleware: load shed at a pressure of %.2f", p))
				if cfg.OnReject == nil {
					return err
				}
				return cfg.OnReject(c, err)
			}
			start := s.now()
			s.inflight.Add(1)
			defer func() {
				s.inflight.Add(-1)
				s.observe(s.now().Sub(start))
			}()
			return next(c)
		}
	}
}

// shedder holds the state of one LoadShed. Its clock and its random source
// are fields, so a test of the package can drive them.
type shedder struct {
	pressure func() float64
	now      func() time.Time
	rand     func() float64

	maxInFlight float64
	target      float64
	alpha       float64
	threshold   float64
	floor       float64

	inflight atomic.Int64
	// ewma holds the bits of the average latency in nanoseconds, so a request
	// updates it without a lock.
	ewma atomic.Uint64
}

func newShedder(maxInFlight int, target time.Duration, alpha, threshold, floor float64, pressure func() float64) *shedder {
	switch {
	case maxInFlight == 0 && target == 0 && pressure == nil:
		panic("middleware: LoadShed needs a criterion; set MaxInFlight, TargetLatency or Pressure")
	case maxInFlight < 0 || target < 0:
		panic("middleware: LoadShed needs a MaxInFlight and a TargetLatency of zero or more")
	case !(alpha >= 0 && alpha <= 1):
		panic(fmt.Sprintf("middleware: LoadShedConfig.Alpha is %v; use a weight from 0 to 1", alpha))
	case !(threshold >= 0 && threshold < 1):
		panic(fmt.Sprintf("middleware: LoadShedConfig.Threshold is %v; use a pressure from 0 to under 1", threshold))
	case !(floor >= 0 && floor < 1):
		panic(fmt.Sprintf("middleware: LoadShedConfig.Floor is %v; use a share from 0 to under 1", floor))
	}
	s := &shedder{
		pressure:    pressure,
		now:         time.Now,
		rand:        rand.Float64,
		maxInFlight: float64(maxInFlight),
		target:      float64(target),
		alpha:       alpha,
		threshold:   threshold,
		floor:       floor,
	}
	if s.alpha == 0 {
		s.alpha = DefaultLoadShedAlpha
	}
	if s.threshold == 0 {
		s.threshold = DefaultLoadShedThreshold
	}
	if s.floor == 0 {
		s.floor = DefaultLoadShedFloor
	}
	return s
}

// admit reports the pressure, and whether the request may pass: every request
// at or below the threshold, and above it all but a share that rises in a
// straight line to 1 - floor at saturation.
func (s *shedder) admit() (float64, bool) {
	p := s.load()
	if p <= s.threshold {
		return p, true
	}
	share := min((p-s.threshold)/(1-s.threshold), 1)
	return p, s.rand() >= share*(1-s.floor)
}

// load is the highest pressure among the criteria, at most 1.
func (s *shedder) load() float64 {
	var p float64
	if s.maxInFlight > 0 {
		p = float64(s.inflight.Load()) / s.maxInFlight
	}
	if s.target > 0 {
		p = max(p, math.Float64frombits(s.ewma.Load())/s.target)
	}
	if s.pressure != nil {
		p = max(p, gauge(s.pressure))
	}
	return min(p, 1)
}

// gauge reads a criterion of the application. A panic, a NaN and a negative
// value all read as 0, so a broken gauge lets traffic through.
func gauge(f func() float64) (p float64) {
	defer func() {
		if recover() != nil {
			p = 0
		}
	}()
	if p = f(); !(p > 0) {
		return 0
	}
	return p
}

// observe folds the latency d into the average. The first request sets it.
func (s *shedder) observe(d time.Duration) {
	if s.target == 0 {
		return
	}
	sample := float64(d)
	for {
		old := s.ewma.Load()
		next := sample
		if old != 0 {
			next = s.alpha*sample + (1-s.alpha)*math.Float64frombits(old)
		}
		if s.ewma.CompareAndSwap(old, math.Float64bits(next)) {
			return
		}
	}
}
