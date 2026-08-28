package middleware

import (
	"fmt"
	"maps"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/dmitrymomot/go-router"
)

const DefaultRateLimitExpiry = 3 * time.Minute

type RateLimitStore[C router.Context] interface {
	Allow(c C, id string) (bool, time.Duration, error)
}

type RateLimitConfig[C router.Context] struct {
	Skip func(c router.Context) bool

	Store RateLimitStore[C]

	KeyFunc func(c C) (string, error)

	OnDeny func(c C, id string, retryAfter time.Duration) error
}

func RateLimit[C router.Context](store RateLimitStore[C]) router.Middleware[C] {
	return RateLimitWithConfig[C](RateLimitConfig[C]{Store: store})
}

func RateLimitWithConfig[C router.Context](cfg RateLimitConfig[C]) router.Middleware[C] {
	if cfg.Store == nil {
		panic("middleware: RateLimitWithConfig needs a Store")
	}
	if cfg.KeyFunc == nil {
		cfg.KeyFunc = func(c C) (string, error) { return ClientIP(c), nil }
	}
	if cfg.OnDeny == nil {
		cfg.OnDeny = denyTooManyRequests[C]
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			id, err := cfg.KeyFunc(c)
			if err != nil {
				return router.ErrForbidden.WithMessage("the client is not identified").WithError(err)
			}

			allowed, retryAfter, err := cfg.Store.Allow(c, id)
			if err != nil {
				return router.ErrInternalServerError.WithError(
					fmt.Errorf("middleware: rate limit store: %w", err))
			}
			if !allowed {
				return cfg.OnDeny(c, id, retryAfter)
			}
			return next(c)
		}
	}
}

func denyTooManyRequests[C router.Context](c C, _ string, retryAfter time.Duration) error {
	if retryAfter > 0 {
		c.Response().Header().Set(router.HeaderRetryAfter,
			strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
	}
	return router.ErrTooManyRequests
}

func NewMemoryStore[C router.Context](rate float64, burst int, expiresIn time.Duration) RateLimitStore[C] {
	if rate <= 0 {
		panic("middleware: NewMemoryStore needs a rate above zero")
	}
	if burst < 1 {
		burst = 1
	}
	if expiresIn <= 0 {
		expiresIn = DefaultRateLimitExpiry
	}
	return &memoryStore[C]{
		visitors:  make(map[string]bucket),
		rate:      rate,
		burst:     float64(burst),
		expiresIn: expiresIn,
		swept:     time.Now(),
	}
}

type bucket struct {
	seen time.Time

	tokens float64
}

type memoryStore[C router.Context] struct {
	mu        sync.Mutex
	visitors  map[string]bucket
	swept     time.Time
	rate      float64
	burst     float64
	expiresIn time.Duration
}

func (s *memoryStore[C]) Allow(_ C, id string) (bool, time.Duration, error) {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(now)

	b, ok := s.visitors[id]
	if !ok {
		b.tokens = s.burst
	} else {
		b.tokens = min(s.burst, b.tokens+now.Sub(b.seen).Seconds()*s.rate)
	}
	b.seen = now

	if b.tokens < 1 {
		s.visitors[id] = b
		wait := time.Duration((1 - b.tokens) / s.rate * float64(time.Second))
		return false, wait, nil
	}

	b.tokens--
	s.visitors[id] = b
	return true, 0, nil
}

func (s *memoryStore[C]) sweep(now time.Time) {
	if now.Sub(s.swept) < s.expiresIn {
		return
	}
	s.swept = now
	maps.DeleteFunc(s.visitors, func(_ string, b bucket) bool {
		idle := now.Sub(b.seen)
		return idle > s.expiresIn && b.tokens+idle.Seconds()*s.rate >= s.burst
	})
}
