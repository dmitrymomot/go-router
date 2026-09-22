package middleware

import (
	"cmp"
	"fmt"
	"hash/maphash"
	"math"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dmitrymomot/go-router"
)

// The defaults of the memory store: how long it keeps a client it has not
// heard from, and how many clients it tracks.
const (
	DefaultRateLimitExpiry     = 3 * time.Minute
	DefaultRateLimitMaxEntries = 64 << 10
)

// RateLimitStore decides whether one client may make one more request. Allow
// reports whether the request passes and, when it does not, how long the
// client has to wait. An error from Allow reaches the client as a 500.
//
// [NewRateLimitMemoryStore] holds the counters in this process. Implement the
// interface to share them across several, such as through Redis.
type RateLimitStore interface {
	Allow(c router.Context, id string) (bool, time.Duration, error)
}

// RateLimitConfig configures [RateLimitWithConfig].
//
// Store is required. Client says who the client is, and a nil one takes the
// address that [ClientIP] reports, with an IPv6 address cut to its /64, since
// one host holds a whole /64. A NAT64 address counts as the IPv4 client
// behind it. Report the account id instead to limit a
// signed-in user rather than an address. An empty id is one client like any
// other.
//
// OnDeny answers a refused request itself, in place of the 429 with
// Retry-After.
type RateLimitConfig[C router.Context] struct {
	Skip   func(c C) bool
	Store  RateLimitStore
	Client func(c C) string
	OnDeny func(c C, id string, retryAfter time.Duration) error
}

// RateLimitMemoryStoreConfig configures [NewRateLimitMemoryStoreWithConfig].
// Rate is the requests per second that refill the bucket, and it is required.
// Burst is how many the bucket holds, and zero takes one. ExpiresIn is how
// long a client that went quiet is remembered, and zero takes
// [DefaultRateLimitExpiry].
//
// MaxEntries caps how many clients the store tracks, so a flood of one-off
// clients cannot exhaust the memory, and zero takes
// [DefaultRateLimitMaxEntries]. A full store forgets the client whose entry
// expires first, which is the one closest to a full bucket, to make room for
// a new one. So a flood of new clients can refill the bucket of a quiet one
// early, and it can never lock a new client out.
type RateLimitMemoryStoreConfig struct {
	Rate       float64
	Burst      int
	ExpiresIn  time.Duration
	MaxEntries int
}

// RateLimit refuses a request that store turns away, with a 429 and a
// Retry-After header. The client is the address that [ClientIP] reports, an
// IPv6 address cut to its /64, so put [RealIP] in front where a proxy is.
//
// See Order in the package doc for where it goes.
//
// RateLimit panics if store is nil.
func RateLimit[C router.Context](store RateLimitStore) router.Middleware[C] {
	return RateLimitWithConfig(RateLimitConfig[C]{Store: store})
}

// RateLimitWithConfig is [RateLimit] with a configuration.
//
// RateLimitWithConfig panics on a nil Store.
func RateLimitWithConfig[C router.Context](cfg RateLimitConfig[C]) router.Middleware[C] {
	if cfg.Store == nil {
		panic("middleware: RateLimitWithConfig needs a Store")
	}
	if cfg.Client == nil {
		cfg.Client = func(c C) string { return clientNetwork(c) }
	}
	if cfg.OnDeny == nil {
		cfg.OnDeny = denyTooManyRequests[C]
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			id := cfg.Client(c)
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

// The NAT64 prefixes of RFC 6052 and RFC 8215. An address in them stands for
// an IPv4 client, so it is not cut to its /64.
var (
	nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")
	nat64LocalUse  = netip.MustParsePrefix("64:ff9b:1::/48")
)

// clientNetwork reports the address of the client, with an IPv6 address cut
// to its /64. An address in the NAT64 prefix 64:ff9b::/96 is reported as the
// IPv4 address it carries, and one in 64:ff9b:1::/48 whole, since the network
// picks where in it the IPv4 address sits. A RemoteAddr that holds no IP
// address is reported as it stands.
func clientNetwork(c router.Context) string {
	addr, ok := ClientAddr(c)
	switch {
	case !ok:
		return ClientIP(c)
	case nat64WellKnown.Contains(addr):
		b := addr.As16()
		return netip.AddrFrom4([4]byte(b[12:])).String()
	case nat64LocalUse.Contains(addr):
		return addr.String()
	case addr.Is6():
		return netip.PrefixFrom(addr, 64).Masked().String()
	}
	return addr.String()
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

// NewRateLimitMemoryStore builds a token bucket store in this process: rate
// tokens per second, burst tokens at most, and a client forgotten after
// expiresIn of quiet. A burst of zero takes one, and an expiresIn of zero
// takes [DefaultRateLimitExpiry].
//
// The counters live in one process, so several instances each hold their own.
//
// NewRateLimitMemoryStore panics on a rate that is not above zero, and on a
// negative burst or expiresIn.
func NewRateLimitMemoryStore(rate float64, burst int, expiresIn time.Duration) RateLimitStore {
	return NewRateLimitMemoryStoreWithConfig(RateLimitMemoryStoreConfig{
		Rate:      rate,
		Burst:     burst,
		ExpiresIn: expiresIn,
	})
}

// NewRateLimitMemoryStoreWithConfig is [NewRateLimitMemoryStore] with a
// configuration, which also caps how many clients the store tracks.
//
// NewRateLimitMemoryStoreWithConfig panics on a Rate that is not above zero,
// and on a negative Burst, ExpiresIn or MaxEntries.
func NewRateLimitMemoryStoreWithConfig(cfg RateLimitMemoryStoreConfig) RateLimitStore {
	if cfg.Rate <= 0 || math.IsNaN(cfg.Rate) || math.IsInf(cfg.Rate, 0) {
		panic("middleware: NewRateLimitMemoryStore needs a Rate above zero")
	}
	if cfg.Burst < 0 {
		panic("middleware: NewRateLimitMemoryStore needs a Burst of zero or more")
	}
	if cfg.ExpiresIn < 0 {
		panic("middleware: NewRateLimitMemoryStore needs an ExpiresIn of zero or more")
	}
	if cfg.MaxEntries < 0 {
		panic("middleware: NewRateLimitMemoryStore needs a MaxEntries of zero or more")
	}
	return &memoryStore{
		seed:       maphash.MakeSeed(),
		rate:       cfg.Rate,
		burst:      float64(cmp.Or(cfg.Burst, 1)),
		expiresIn:  cmp.Or(cfg.ExpiresIn, DefaultRateLimitExpiry),
		maxEntries: int64(cmp.Or(cfg.MaxEntries, DefaultRateLimitMaxEntries)),
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

type memoryStore struct {
	shards        [memoryStoreShards]memoryShard
	seed          maphash.Seed
	cleanupCursor atomic.Uint64
	entries       atomic.Int64
	rate          float64
	burst         float64
	expiresIn     time.Duration
	maxEntries    int64
}

func (s *memoryStore) Allow(_ router.Context, id string) (bool, time.Duration, error) {
	shard := s.shard(id)
	if other := &s.shards[(s.cleanupCursor.Add(1)-1)%memoryStoreShards]; other != shard {
		other.mu.Lock()
		s.cleanup(other, time.Now())
		other.mu.Unlock()
	}

	shard.mu.Lock()
	defer shard.mu.Unlock()
	s.cleanup(shard, time.Now())

	b := shard.visitors[id]
	for b == nil && !s.reserve() {
		// The store is full. Forget the client that expires first, with no
		// lock held, since that takes the lock of each shard in turn.
		shard.mu.Unlock()
		s.evictFirst()
		shard.mu.Lock()
		b = shard.visitors[id]
	}
	now := time.Now()
	if b == nil {
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

// evictFirst drops the entry that expires first across the shards. It holds
// one lock at a time, so the entry it drops may no longer be the first by the
// time it drops it, which only matters to a store that is full anyway.
func (s *memoryStore) evictFirst() {
	var first *memoryShard
	var expires time.Time
	for i := range s.shards {
		shard := &s.shards[i]
		shard.mu.Lock()
		if len(shard.expiry) > 0 && (first == nil || shard.expiry[0].expires.Before(expires)) {
			first, expires = shard, shard.expiry[0].expires
		}
		shard.mu.Unlock()
	}
	if first == nil {
		return
	}
	first.mu.Lock()
	defer first.mu.Unlock()
	if len(first.expiry) == 0 {
		return
	}
	b := first.heapPop()
	if first.visitors[b.id] == b {
		delete(first.visitors, b.id)
		s.entries.Add(-1)
	}
}

func (s *memoryStore) reserve() bool {
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

func (s *memoryStore) cleanup(shard *memoryShard, now time.Time) {
	if removed := shard.cleanup(now); removed > 0 {
		s.entries.Add(-int64(removed))
	}
}

func (s *memoryStore) shard(id string) *memoryShard {
	i := maphash.String(s.seed, id) % memoryStoreShards
	return &s.shards[i]
}

func (s *memoryStore) schedule(shard *memoryShard, b *bucket, now time.Time) {
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
