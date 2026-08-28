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

// DefaultRateLimitExpiry is how long [NewMemoryStore] keeps a visitor that
// stopped sending requests.
const DefaultRateLimitExpiry = 3 * time.Minute

// RateLimitStore decides whether a request may proceed. Allow reports that,
// the wait until the next one may, and a failure of the store itself.
//
// The store takes the application context, so one that reads a connection off
// it reaches the value as the type it is, with no assertion out of an untyped
// store:
//
//	func (s *redisStore) Allow(c *app.Context, id string) (bool, time.Duration, error) {
//		return s.take(c, c.Redis, id)
//	}
//
// The wait is how long the client waits until one request is free again. Allow
// reports it without taking a token, so a client that keeps knocking while it
// is denied does not push its own answer further away.
type RateLimitStore[C router.Context] interface {
	Allow(c C, id string) (bool, time.Duration, error)
}

// RateLimitConfig configures [RateLimitWithConfig]. It is generic over the
// application context, because the store and the callbacks receive it.
type RateLimitConfig[C router.Context] struct {
	// Skip passes a request straight to the next handler when it returns true.
	Skip func(c router.Context) bool

	// Store decides whether a request may proceed. It has no default.
	Store RateLimitStore[C]

	// KeyFunc returns the identity that the limit counts against. It defaults
	// to [ClientIP], and an error from it answers 403.
	KeyFunc func(c C) (string, error)

	// OnDeny answers a request that the store refused. It defaults to setting
	// Retry-After and returning [router.ErrTooManyRequests].
	OnDeny func(c C, id string, retryAfter time.Duration) error
}

// RateLimit is [RateLimitWithConfig] with the store alone, which counts
// against [ClientIP]:
//
//	r.Use(middleware.RateLimit(middleware.NewMemoryStore[Ctx](10, 30, time.Minute)))
//
// The store names the context type, so this call needs no type argument.
func RateLimit[C router.Context](store RateLimitStore[C]) router.Middleware[C] {
	return RateLimitWithConfig[C](RateLimitConfig[C]{Store: store})
}

// RateLimitWithConfig answers a request that the store refused with
// [router.ErrTooManyRequests] and a Retry-After header.
//
// It panics without a store, because a rate limit that lets everything through
// is worse than none: it reads as a limit at the call site and answers as no
// limit at run time.
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

// denyTooManyRequests is the default OnDeny. It sets Retry-After, rounded up
// to the next second, because the header carries whole seconds and a wait of
// zero reads as an invitation to try again at once.
func denyTooManyRequests[C router.Context](c C, _ string, retryAfter time.Duration) error {
	if retryAfter > 0 {
		c.Response().Header().Set(router.HeaderRetryAfter,
			strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
	}
	return router.ErrTooManyRequests
}

// NewMemoryStore returns a [RateLimitStore] that keeps one token bucket per
// identity in this process. It allows rate requests per second and burst
// requests at once, and it forgets an identity that sends nothing for
// expiresIn once the bucket of that identity has refilled. A rate of zero or
// less panics, a burst below one reads as one, and an expiry of zero or less
// uses [DefaultRateLimitExpiry].
//
//	middleware.NewMemoryStore[Ctx](10, 30, time.Minute)   // 10/s, 30 at once
//
// The store evicts a stale identity inside Allow, once per expiry window,
// which is what leaves it with no background goroutine and nothing to close.
// The price is one walk of the map: with n identities it is O(n) once every
// expiresIn, and every other call is O(1).
//
// An identity that spent its burst stays in the map until the burst refills,
// which is burst/rate seconds, however short expiresIn is. A bucket comes back
// full, so forgetting one that still owes the client tokens would hand the
// client a burst that the wait had not earned: with a thousand requests an
// hour and the three-minute default, a client that paused for three minutes
// would start over twenty times an hour.
//
// The buckets live in this process, so two instances of a service hold two
// limits and a client reaches the sum of them. Write a store over a shared
// cache for a limit that a fleet keeps together.
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

// bucket is the token bucket of one identity.
type bucket struct {
	// seen is when the bucket last refilled, which is the last request of the
	// identity and the age that the sweep reads.
	seen time.Time

	// tokens is what the bucket holds, in requests.
	tokens float64
}

// memoryStore is the store that [NewMemoryStore] returns.
type memoryStore[C router.Context] struct {
	mu        sync.Mutex
	visitors  map[string]bucket
	swept     time.Time
	rate      float64
	burst     float64
	expiresIn time.Duration
}

// Allow implements [RateLimitStore].
func (s *memoryStore[C]) Allow(_ C, id string) (bool, time.Duration, error) {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(now)

	b, ok := s.visitors[id]
	if !ok {
		b.tokens = s.burst
	} else {
		// The bucket refills with the time that passed, up to the burst.
		b.tokens = min(s.burst, b.tokens+now.Sub(b.seen).Seconds()*s.rate)
	}
	b.seen = now

	if b.tokens < 1 {
		// A denied request takes no token, so a client that keeps knocking
		// waits the same time as one that waits.
		s.visitors[id] = b
		wait := time.Duration((1 - b.tokens) / s.rate * float64(time.Second))
		return false, wait, nil
	}

	b.tokens--
	s.visitors[id] = b
	return true, 0, nil
}

// sweep drops the identities that stopped sending requests. It runs once per
// expiry window, inside the lock that Allow already holds.
//
// It keeps a bucket that has not refilled to the burst. Allow starts an
// identity it does not find at a full burst, so dropping a bucket that still
// owes the client tokens would hand the client those tokens, and the sweep of
// the store would be the way around the limit that the store keeps. What it
// drops is a bucket that refilled, which answers exactly as the one that Allow
// makes in its place.
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
