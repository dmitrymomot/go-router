package middleware

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router"
)

func newTestMemoryStore(t *testing.T, rate float64, burst int, expiresIn time.Duration) *memoryStore[router.Context] {
	t.Helper()
	s, ok := NewMemoryStore[router.Context](rate, burst, expiresIn).(*memoryStore[router.Context])
	if !ok {
		t.Fatal("NewMemoryStore no longer returns a *memoryStore")
	}
	return s
}

func newTestMemoryStoreWithConfig(t *testing.T, cfg MemoryStoreConfig) *memoryStore[router.Context] {
	t.Helper()
	s, ok := NewMemoryStoreWithConfig[router.Context](cfg).(*memoryStore[router.Context])
	if !ok {
		t.Fatal("NewMemoryStoreWithConfig no longer returns a *memoryStore")
	}
	return s
}

func take(t *testing.T, s *memoryStore[router.Context], id string) bool {
	t.Helper()
	allowed, _, err := s.Allow(nil, id)
	if err != nil {
		t.Fatalf("Allow(%q): %v", id, err)
	}
	return allowed
}

func memoryStoreSize(s *memoryStore[router.Context]) int {
	total := 0
	for i := range s.shards {
		shard := &s.shards[i]
		shard.mu.Lock()
		total += len(shard.visitors)
		shard.mu.Unlock()
	}
	return total
}

func memoryStoreExpirySize(s *memoryStore[router.Context]) int {
	total := 0
	for i := range s.shards {
		shard := &s.shards[i]
		shard.mu.Lock()
		total += len(shard.expiry)
		shard.mu.Unlock()
	}
	return total
}

func sameShardID(s *memoryStore[router.Context], id string, n int) string {
	want := s.shard(id)
	for i := n; ; i++ {
		candidate := fmt.Sprintf("client-%d", i)
		if candidate != id && s.shard(candidate) == want {
			return candidate
		}
	}
}

func shardID(s *memoryStore[router.Context], shard int) string {
	for i := 0; ; i++ {
		candidate := fmt.Sprintf("shard-%d-client-%d", shard, i)
		if s.shard(candidate) == &s.shards[shard] {
			return candidate
		}
	}
}

func shardIndex(s *memoryStore[router.Context], id string) int {
	want := s.shard(id)
	for i := range s.shards {
		if want == &s.shards[i] {
			return i
		}
	}
	panic("unreachable")
}

func TestMemoryStoreSweepDropsARefilledBucket(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestMemoryStore(t, 2, 2, time.Second)
		grace := sameShardID(s, "ada", 0)

		take(t, s, "ada")
		take(t, s, grace)
		if got := memoryStoreSize(s); got != 2 {
			t.Fatalf("the store holds %d buckets, want 2", got)
		}

		time.Sleep(2 * time.Second)
		s.cleanupCursor.Store(uint64(shardIndex(s, "ada")))
		take(t, s, "ada")

		if got := memoryStoreSize(s); got != 1 {
			t.Errorf("the store holds %d buckets, want 1: cleanup kept a bucket that had refilled", got)
		}
	})
}

func TestMemoryStoreSweepKeepsABucketThatOwesTokens(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestMemoryStore(t, 0.001, 2, time.Second)
		grace := sameShardID(s, "ada", 0)

		take(t, s, "ada")
		take(t, s, "ada")
		if take(t, s, "ada") {
			t.Fatal("the third request passed a burst of two")
		}

		time.Sleep(2 * time.Second)
		s.cleanupCursor.Store(uint64(shardIndex(s, "ada")))
		take(t, s, grace)

		if got := memoryStoreSize(s); got != 2 {
			t.Fatalf("the store holds %d buckets, want 2: cleanup dropped a bucket that still owed tokens", got)
		}
		if take(t, s, "ada") {
			t.Error("the client passed on a bucket that the store started again, which is the burst it never earned")
		}
	})
}

func TestMemoryStoreCleanupIsBoundedPerRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestMemoryStore(t, 1000, 1, time.Second)
		base := shardID(s, 0)
		ids := []string{base}
		for i := 0; len(ids) < memorySweepBatch+5; i++ {
			id := sameShardID(s, base, i)
			seen := false
			for _, existing := range ids {
				seen = seen || existing == id
			}
			if !seen {
				ids = append(ids, id)
			}
		}
		for _, id := range ids {
			take(t, s, id)
		}

		time.Sleep(2 * time.Second)
		shard := s.shard(base)
		shard.mu.Lock()
		before := len(shard.visitors)
		shard.mu.Unlock()
		s.cleanupCursor.Store(0)
		take(t, s, shardID(s, 1))
		shard.mu.Lock()
		after := len(shard.visitors)
		shard.mu.Unlock()

		if got := before - after; got != memorySweepBatch {
			t.Errorf("cleanup removed %d buckets, want the bounded batch of %d", got, memorySweepBatch)
		}
	})
}

func TestMemoryStoreCleanupAdvancesAcrossShards(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestMemoryStore(t, 1000, 1, time.Second)
		ids := make([]string, memoryStoreShards)
		for i := range ids {
			ids[i] = shardID(s, i)
			take(t, s, ids[i])
		}

		time.Sleep(2 * time.Second)
		s.cleanupCursor.Store(0)
		hot := ids[memoryStoreShards-1]
		for range memoryStoreShards {
			take(t, s, hot)
		}

		if got := memoryStoreSize(s); got != 1 {
			t.Errorf("the store holds %d buckets, want only the active bucket", got)
		}
	})
}

func TestMemoryStoreCleanupAdvancesAcrossShardsConcurrently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestMemoryStore(t, 1000, 1, time.Second)
		ids := make([]string, memoryStoreShards)
		for i := range ids {
			ids[i] = shardID(s, i)
			take(t, s, ids[i])
		}

		time.Sleep(2 * time.Second)
		s.cleanupCursor.Store(0)
		hot := ids[memoryStoreShards-1]
		var failures atomic.Int64
		var wg sync.WaitGroup
		for range memoryStoreShards * 2 {
			wg.Go(func() {
				if _, _, err := s.Allow(nil, hot); err != nil {
					failures.Add(1)
				}
			})
		}
		wg.Wait()

		if got := failures.Load(); got != 0 {
			t.Errorf("Allow returned %d errors, want none", got)
		}
		if got := memoryStoreSize(s); got != 1 {
			t.Errorf("the store holds %d buckets, want only the active bucket", got)
		}
	})
}

func TestMemoryStoreAllowsConcurrentShards(t *testing.T) {
	s := newTestMemoryStore(t, 1000, 10, time.Minute)
	var failures atomic.Int64
	var wg sync.WaitGroup
	for worker := range memoryStoreShards * 2 {
		wg.Go(func() {
			for request := range 200 {
				_, _, err := s.Allow(nil, fmt.Sprintf("client-%d-%d", worker, request))
				if err != nil {
					failures.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if got := failures.Load(); got != 0 {
		t.Errorf("Allow returned %d errors, want none", got)
	}
}

func TestMemoryStoreHasAFiniteDefaultCapacity(t *testing.T) {
	s := newTestMemoryStore(t, 1, 1, time.Minute)
	if got := s.maxEntries; got != DefaultMemoryStoreMaxEntries {
		t.Errorf("max entries = %d, want %d", got, DefaultMemoryStoreMaxEntries)
	}
}

func TestMemoryStoreConfigDefaults(t *testing.T) {
	s := newTestMemoryStoreWithConfig(t, MemoryStoreConfig{Rate: 2})
	if s.burst != 1 {
		t.Errorf("burst = %v, want 1", s.burst)
	}
	if s.expiresIn != DefaultRateLimitExpiry {
		t.Errorf("expiry = %s, want %s", s.expiresIn, DefaultRateLimitExpiry)
	}
	if s.maxEntries != DefaultMemoryStoreMaxEntries {
		t.Errorf("max entries = %d, want %d", s.maxEntries, DefaultMemoryStoreMaxEntries)
	}
	if !take(t, s, "ada") || take(t, s, "ada") {
		t.Error("the default burst did not allow exactly one immediate request")
	}
}

func TestMemoryStoreRejectsNegativeCapacity(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewMemoryStoreWithConfig accepted a negative capacity")
		}
	}()
	newTestMemoryStoreWithConfig(t, MemoryStoreConfig{Rate: 1, MaxEntries: -1})
}

func TestMemoryStoreCapacityFailsClosed(t *testing.T) {
	s := newTestMemoryStoreWithConfig(t, MemoryStoreConfig{
		Rate:       1,
		Burst:      2,
		ExpiresIn:  time.Hour,
		MaxEntries: 2,
	})
	if !take(t, s, "ada") || !take(t, s, "grace") {
		t.Fatal("the store denied an entry before reaching capacity")
	}
	allowed, wait, err := s.Allow(nil, "linus")
	if err != nil {
		t.Fatalf("Allow at capacity: %v", err)
	}
	if allowed {
		t.Fatal("an untracked client passed at capacity")
	}
	if wait != time.Hour {
		t.Errorf("retry after = %s, want %s", wait, time.Hour)
	}
	if !take(t, s, "ada") {
		t.Error("a tracked client lost its remaining token at capacity")
	}
	if take(t, s, "ada") {
		t.Error("a tracked client exceeded its burst at capacity")
	}
	if got := memoryStoreSize(s); got != 2 {
		t.Errorf("the store holds %d buckets, want 2", got)
	}
	if got := s.entries.Load(); got != 2 {
		t.Errorf("entry count = %d, want 2", got)
	}
}

func TestMemoryStoreCapacityRecoversAfterExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestMemoryStoreWithConfig(t, MemoryStoreConfig{
			Rate:       1000,
			Burst:      1,
			ExpiresIn:  time.Second,
			MaxEntries: 1,
		})
		first := shardID(s, 0)
		second := sameShardID(s, first, 0)
		if !take(t, s, first) {
			t.Fatal("the first client was denied")
		}

		time.Sleep(time.Second)
		if !take(t, s, second) {
			t.Fatal("a new client was denied after the old entry expired")
		}
		if got := memoryStoreSize(s); got != 1 {
			t.Errorf("the store holds %d buckets, want 1", got)
		}
		if got := s.entries.Load(); got != 1 {
			t.Errorf("entry count = %d, want 1", got)
		}
	})
}

func TestMemoryStoreCapacityIsRaceSafe(t *testing.T) {
	const (
		maxEntries = 127
		requests   = 2048
	)
	s := newTestMemoryStoreWithConfig(t, MemoryStoreConfig{
		Rate:       1,
		Burst:      1,
		ExpiresIn:  time.Hour,
		MaxEntries: maxEntries,
	})
	start := make(chan struct{})
	var allowed atomic.Int64
	var failures atomic.Int64
	var wg sync.WaitGroup
	for i := range requests {
		wg.Go(func() {
			<-start
			ok, _, err := s.Allow(nil, strconv.Itoa(i))
			if err != nil {
				failures.Add(1)
			}
			if ok {
				allowed.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()

	if got := failures.Load(); got != 0 {
		t.Errorf("Allow returned %d errors, want none", got)
	}
	if got := allowed.Load(); got != maxEntries {
		t.Errorf("allowed requests = %d, want %d", got, maxEntries)
	}
	if got := memoryStoreSize(s); got != maxEntries {
		t.Errorf("the store holds %d buckets, want %d", got, maxEntries)
	}
	if got := s.entries.Load(); got != maxEntries {
		t.Errorf("entry count = %d, want %d", got, maxEntries)
	}
}

func TestMemoryStoreCardinalityStaysBoundedForUniqueIDs(t *testing.T) {
	const (
		maxEntries = 256
		requests   = 100000
	)
	s := newTestMemoryStoreWithConfig(t, MemoryStoreConfig{
		Rate:       1,
		Burst:      1,
		ExpiresIn:  time.Hour,
		MaxEntries: maxEntries,
	})
	allowed := 0
	for i := range requests {
		ok, _, err := s.Allow(nil, strconv.Itoa(i))
		if err != nil {
			t.Fatalf("Allow(%d): %v", i, err)
		}
		if ok {
			allowed++
		}
	}
	if allowed != maxEntries {
		t.Errorf("allowed requests = %d, want %d", allowed, maxEntries)
	}
	if got := memoryStoreSize(s); got != maxEntries {
		t.Errorf("the store holds %d buckets, want %d", got, maxEntries)
	}
	if got := memoryStoreExpirySize(s); got != maxEntries {
		t.Errorf("the expiry heaps hold %d buckets, want %d", got, maxEntries)
	}
	if got := s.entries.Load(); got != maxEntries {
		t.Errorf("entry count = %d, want %d", got, maxEntries)
	}
}

func BenchmarkMemoryStoreAtCapacityUniqueIDs(b *testing.B) {
	const maxEntries = 1024
	s := NewMemoryStoreWithConfig[router.Context](MemoryStoreConfig{
		Rate:       1,
		Burst:      1,
		ExpiresIn:  time.Hour,
		MaxEntries: maxEntries,
	}).(*memoryStore[router.Context])
	for i := range maxEntries {
		if allowed, _, err := s.Allow(nil, strconv.Itoa(i)); err != nil || !allowed {
			b.Fatalf("fill request %d: allowed = %t, err = %v", i, allowed, err)
		}
	}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		allowed, _, err := s.Allow(nil, strconv.Itoa(i+maxEntries))
		if err != nil || allowed {
			b.Fatalf("request %d: allowed = %t, err = %v", i, allowed, err)
		}
	}
	if got := memoryStoreSize(s); got != maxEntries {
		b.Fatalf("the store holds %d buckets, want %d", got, maxEntries)
	}
}
