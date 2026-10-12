package middleware

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/dmitrymomot/go-router"
)

// DefaultThrottleBacklogTimeout is how long a request waits in the backlog of
// [Throttle] when BacklogTimeout is zero.
const DefaultThrottleBacklogTimeout = 30 * time.Second

// ThrottleConfig configures [ThrottleWithConfig]. Limit is the number of
// requests that run at once, and is required. Backlog is how many more may
// wait for a slot; zero means none wait. BacklogTimeout is how long one waits,
// and zero takes [DefaultThrottleBacklogTimeout]. Status is the answer to a
// request that is turned away: 429 or 503, and zero takes 429. RetryAfter,
// when set, goes into the Retry-After header of a rejection, in whole seconds.
//
// OnReject answers a request that is turned away. The Retry-After header is
// already set when it runs, and err is the error of Status that the
// middleware would report. Return an error to report it in place of the
// request, or nil to say that OnReject wrote the answer itself; either way the
// request goes no further. A nil OnReject reports err.
type ThrottleConfig[C router.Context] struct {
	Skip           func(c C) bool
	OnReject       func(c C, err error) error
	Limit          int
	Backlog        int
	BacklogTimeout time.Duration
	RetryAfter     time.Duration
	Status         int
}

// Throttle caps the number of requests that run at once at limit, with no
// backlog: the next one is turned away with a 429. Use [ThrottleWithConfig]
// for a backlog, a 503, or a Retry-After.
//
// Unlike [RateLimit], which counts requests over time per client, Throttle
// protects a resource from too many requests in flight. The count lives in one
// process.
//
// See Order in the package doc for where it goes.
//
// Throttle panics on a limit under one.
func Throttle[C router.Context](limit int) router.Middleware[C] {
	return ThrottleWithConfig(ThrottleConfig[C]{Limit: limit})
}

// ThrottleWithConfig is [Throttle] with a configuration. A request waits in the
// backlog until a slot frees, the BacklogTimeout passes, or its context ends;
// the last two reject it.
//
// ThrottleWithConfig panics on a Limit under one, on a negative Backlog,
// BacklogTimeout or RetryAfter, and on a Status other than 429 and 503.
func ThrottleWithConfig[C router.Context](cfg ThrottleConfig[C]) router.Middleware[C] {
	if cfg.Limit < 1 {
		panic(fmt.Sprintf("middleware: ThrottleConfig.Limit is %d; set the number of requests "+
			"that may run at once, one or more", cfg.Limit))
	}
	if cfg.Backlog < 0 || cfg.BacklogTimeout < 0 || cfg.RetryAfter < 0 {
		panic("middleware: ThrottleConfig needs a Backlog, BacklogTimeout and RetryAfter of zero or more")
	}
	switch cfg.Status {
	case 0:
		cfg.Status = http.StatusTooManyRequests
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
	default:
		panic(fmt.Sprintf("middleware: ThrottleConfig.Status is %d; use 429 or 503", cfg.Status))
	}
	if cfg.BacklogTimeout == 0 {
		cfg.BacklogTimeout = DefaultThrottleBacklogTimeout
	}
	retryAfter := ""
	if cfg.RetryAfter > 0 {
		secs := int64((cfg.RetryAfter + time.Second - 1) / time.Second)
		retryAfter = strconv.FormatInt(secs, 10)
	}

	// admitted holds every request that runs or waits, running holds the ones
	// that run.
	admitted := make(chan struct{}, cfg.Limit+cfg.Backlog)
	running := make(chan struct{}, cfg.Limit)

	reject := func(c C) error {
		if retryAfter != "" {
			c.Response().Header().Set(router.HeaderRetryAfter, retryAfter)
		}
		err := router.ErrTooManyRequests
		if cfg.Status == http.StatusServiceUnavailable {
			err = router.ErrServiceUnavailable
		}
		if cfg.OnReject != nil {
			return cfg.OnReject(c, err)
		}
		return err
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}
			select {
			case admitted <- struct{}{}:
			default:
				return reject(c)
			}
			defer func() { <-admitted }()

			select {
			case running <- struct{}{}:
			default:
				if cfg.Backlog == 0 {
					return reject(c)
				}
				timer := time.NewTimer(cfg.BacklogTimeout)
				defer timer.Stop()
				select {
				case running <- struct{}{}:
				case <-timer.C:
					return reject(c)
				case <-c.Request().Context().Done():
					return reject(c)
				}
			}
			defer func() { <-running }()

			return next(c)
		}
	}
}
