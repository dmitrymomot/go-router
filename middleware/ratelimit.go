package middleware

import (
	"fmt"
	"hash/maphash"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dmitrymomot/go-router"
)

// The defaults of the memory store: how long it keeps a client it has not
// heard from, and how many clients it tracks.
const (
	DefaultRateLimitExpiry       = 3 * time.Minute
	DefaultMemoryStoreMaxEntries = 64 << 10
)

// RateLimitStore decides whether one client may make one more request. Allow
// reports whether the request passes and, when it does not, how long the
// client has to wait. An error from Allow reaches the client as a 500.
//
// [NewMemoryStore] holds the counters in this process. Implement the interface
// to share them across several, such as through Redis.
type RateLimitStore[C router.Context] interface {
	Allow(c C, id string) (bool, time.Duration, error)
}

// RateLimitConfig configures [RateLimitWithConfig].
//
// Store is required. KeyFunc says who the client is, and a nil one takes
// [ClientIP]; return the account id instead to limit a signed-in user rather
// than an address. OnDeny answers a refused request itself, in place of the
// 429 with Retry-After.
type RateLimitConfig[C router.Context] struct {
	Skip    func(c router.Context) bool
	Store   RateLimitStore[C]
	KeyFunc func(c C) (string, error)
	OnDeny  func(c C, id string, retryAfter time.Duration) error
}

// MemoryStoreConfig configures [NewMemoryStoreWithConfig]. Rate is the
// requests per second that refill the bucket, and Burst is how many the bucket
// holds. ExpiresIn is how long a client that went quiet is remembered, and
// zero takes [DefaultRateLimitExpiry].
type MemoryStoreConfig struct {
	Rate      float64
	Burst     int
	ExpiresIn time.Duration

	// MaxEntries caps how many keys the store tracks, so that a flood of
	// one-off keys cannot exhaust memory. Zero means
	// DefaultMemoryStoreMaxEntries.
	//
	// A full store fails closed: an id it has never seen is denied for
	// ExpiresIn instead of admitted. With the default ClientIP key that means
	// every first-time visitor is turned away with 429 while the table stays
	// full, so raise the cap if that trade is the wrong way round.
	MaxEntries int
}

// RateLimit refuses a request that store turns away, with a 429 and a
// Retry-After header. The client is the address that [ClientIP] reports, so
// put [RealIP] in front where a proxy is.
//
// RateLimit panics if store is nil.
func RateLimit[C router.Context](store RateLimitStore[C]) router.Middleware[C] {
	return RateLimitWithConfig[C](RateLimitConfig[C]{Store: store})
}

// RateLimitWithConfig is [RateLimit] with a configuration. A KeyFunc that
// reports an error refuses the request with a 403.
//
// RateLimitWithConfig panics on a nil Store.
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
		seconds := retryAfter / time.Second
		if retryAfter%time.Second != 0 {
			seconds++
		}
		c.Response().Header().Set(router.HeaderRetryAfter, strconv.FormatInt(int64(seconds), 10))
	}
	return router.ErrTooManyRequests
}

// NewMemoryStore builds a token bucket store in this process: rate tokens per
// second, burst tokens at most, and a client forgotten after expiresIn of
// quiet. An expiresIn of zero or less takes [DefaultRateLimitExpiry].
//
// The counters live in one process, so several instances each hold their own.
//
// NewMemoryStore panics on a rate that is not above zero.
func NewMemoryStore[C router.Context](rate float64, burst int, expiresIn time.Duration) RateLimitStore[C] {
	return NewMemoryStoreWithConfig[C](MemoryStoreConfig{
		Rate:      rate,
		Burst:     burst,
		ExpiresIn: expiresIn,
	})
}

// NewMemoryStoreWithConfig is [NewMemoryStore] with a configuration, which
// also caps how many clients the store tracks.
//
// NewMemoryStoreWithConfig panics on a rate that is not above zero and on a
// negative MaxEntries.
func NewMemoryStoreWithConfig[C router.Context](cfg MemoryStoreConfig) RateLimitStore[C] {
	if cfg.Rate <= 0 || math.IsNaN(cfg.Rate) || math.IsInf(cfg.Rate, 0) {
		panic("middleware: NewMemoryStore needs a rate above zero")
	}
	if cfg.Burst < 1 {
		cfg.Burst = 1
	}
	if cfg.ExpiresIn <= 0 {
		cfg.ExpiresIn = DefaultRateLimitExpiry
	}
	if cfg.MaxEntries < 0 {
		panic("middleware: NewMemoryStore needs max entries above zero")
	}
	if cfg.MaxEntries == 0 {
		cfg.MaxEntries = DefaultMemoryStoreMaxEntries
	}
	return &memoryStore[C]{
		seed:       maphash.MakeSeed(),
		rate:       cfg.Rate,
		burst:      float64(cfg.Burst),
		expiresIn:  cfg.ExpiresIn,
		maxEntries: int64(cfg.MaxEntries),
	}
}

type bucket struct {
	id        string
	seen      time.Time
	expires   time.Time
	tokens    float64
	heapIndex int
}

const (
	memoryStoreShards = 32
	memorySweepBatch  = 16
)

type memoryShard struct {
	mu       sync.Mutex
	visitors map[string]*bucket
	expiry   []*bucket
}

type memoryStore[C router.Context] struct {
	shards        [memoryStoreShards]memoryShard
	seed          maphash.Seed
	cleanupCursor atomic.Uint64
	entries       atomic.Int64
	rate          float64
	burst         float64
	expiresIn     time.Duration
	maxEntries    int64
}

func (s *memoryStore[C]) Allow(_ C, id string) (bool, time.Duration, error) {
	shard := s.shard(id)
	cleanup := &s.shards[(s.cleanupCursor.Add(1)-1)%memoryStoreShards]

	if cleanup != shard {
		cleanup.mu.Lock()
		s.cleanup(cleanup, time.Now())
		cleanup.mu.Unlock()
	}

	shard.mu.Lock()
	defer shard.mu.Unlock()
	now := time.Now()
	if cleanup == shard {
		s.cleanup(shard, now)
	}

	b := shard.visitors[id]
	if b == nil {
		if !s.reserve() {
			if cleanup != shard {
				s.cleanup(shard, now)
			}
			if !s.reserve() {
				return false, s.expiresIn, nil
			}
		}
		if shard.visitors == nil {
			shard.visitors = make(map[string]*bucket)
		}
		b = &bucket{id: id, heapIndex: -1}
		b.tokens = s.burst
		shard.visitors[id] = b
	} else {
		b.tokens = min(s.burst, b.tokens+now.Sub(b.seen).Seconds()*s.rate)
	}
	b.seen = now

	allowed := b.tokens >= 1
	var wait time.Duration
	if allowed {
		b.tokens--
	} else {
		wait = tokenDuration(1-b.tokens, s.rate)
	}
	s.schedule(shard, b, now)
	return allowed, wait, nil
}

func (s *memoryStore[C]) reserve() bool {
	for {
		entries := s.entries.Load()
		if entries >= s.maxEntries {
			return false
		}
		if s.entries.CompareAndSwap(entries, entries+1) {
			return true
		}
	}
}

func (s *memoryStore[C]) cleanup(shard *memoryShard, now time.Time) {
	if removed := shard.cleanup(now); removed > 0 {
		s.entries.Add(-int64(removed))
	}
}

func (s *memoryStore[C]) shard(id string) *memoryShard {
	i := maphash.String(s.seed, id) % memoryStoreShards
	return &s.shards[i]
}

func (s *memoryStore[C]) schedule(shard *memoryShard, b *bucket, now time.Time) {
	lifetime := s.expiresIn
	if refill := tokenDuration(s.burst-b.tokens, s.rate); refill > lifetime {
		lifetime = refill
	}
	b.expires = now.Add(lifetime)
	if b.heapIndex < 0 {
		shard.heapPush(b)
		return
	}
	shard.heapFix(b.heapIndex)
}

func tokenDuration(tokens, rate float64) time.Duration {
	if tokens <= 0 {
		return 0
	}
	nanos := tokens / rate * float64(time.Second)
	if nanos >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(math.Ceil(nanos))
}

func (s *memoryShard) cleanup(now time.Time) int {
	removed := 0
	for range memorySweepBatch {
		if len(s.expiry) == 0 || s.expiry[0].expires.After(now) {
			return removed
		}
		b := s.heapPop()
		if s.visitors[b.id] == b {
			delete(s.visitors, b.id)
			removed++
		}
	}
	return removed
}

func (s *memoryShard) heapPush(b *bucket) {
	b.heapIndex = len(s.expiry)
	s.expiry = append(s.expiry, b)
	s.heapUp(b.heapIndex)
}

func (s *memoryShard) heapPop() *bucket {
	b := s.expiry[0]
	last := len(s.expiry) - 1
	if last == 0 {
		s.expiry = s.expiry[:0]
		b.heapIndex = -1
		return b
	}
	s.expiry[0] = s.expiry[last]
	s.expiry[0].heapIndex = 0
	s.expiry[last] = nil
	s.expiry = s.expiry[:last]
	b.heapIndex = -1
	s.heapDown(0)
	return b
}

func (s *memoryShard) heapFix(i int) {
	if !s.heapDown(i) {
		s.heapUp(i)
	}
}

func (s *memoryShard) heapUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if !s.expiry[i].expires.Before(s.expiry[parent].expires) {
			return
		}
		s.heapSwap(i, parent)
		i = parent
	}
}

func (s *memoryShard) heapDown(i int) bool {
	start := i
	for {
		left := 2*i + 1
		if left >= len(s.expiry) {
			break
		}
		smallest := left
		if right := left + 1; right < len(s.expiry) &&
			s.expiry[right].expires.Before(s.expiry[left].expires) {
			smallest = right
		}
		if !s.expiry[smallest].expires.Before(s.expiry[i].expires) {
			break
		}
		s.heapSwap(i, smallest)
		i = smallest
	}
	return i != start
}

func (s *memoryShard) heapSwap(i, j int) {
	s.expiry[i], s.expiry[j] = s.expiry[j], s.expiry[i]
	s.expiry[i].heapIndex = i
	s.expiry[j].heapIndex = j
}
