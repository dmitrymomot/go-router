package middleware

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func newTestMemoryStore(t *testing.T, rate float64, burst int, maxAge time.Duration) *memoryStore {
	t.Helper()
	s, ok := NewRateLimitMemoryStore(rate, burst, maxAge).(*memoryStore)
	if !ok {
		t.Fatal("NewRateLimitMemoryStore no longer returns a *memoryStore")
	}
	return s
}

func newTestMemoryStoreWithConfig(t *testing.T, cfg RateLimitMemoryStoreConfig) *memoryStore {
	t.Helper()
	s, ok := NewRateLimitMemoryStoreWithConfig(cfg).(*memoryStore)
	if !ok {
		t.Fatal("NewRateLimitMemoryStoreWithConfig no longer returns a *memoryStore")
	}
	return s
}

func take(t *testing.T, s *memoryStore, id string) bool {
	t.Helper()
	allowed, _, err := s.Allow(nil, id)
	if err != nil {
		t.Fatalf("Allow(%q): %v", id, err)
	}
	return allowed
}

func memoryStoreSize(s *memoryStore) int {
	total := 0
	for i := range s.shards {
		shard := &s.shards[i]
		shard.mu.Lock()
		total += len(shard.visitors)
		shard.mu.Unlock()
	}
	return total
}

func memoryStoreExpirySize(s *memoryStore) int {
	total := 0
	for i := range s.shards {
		shard := &s.shards[i]
		shard.mu.Lock()
		total += len(shard.expiry)
		shard.mu.Unlock()
	}
	return total
}

func sameShardID(s *memoryStore, id string, n int) string {
	want := s.shard(id)
	for i := n; ; i++ {
		candidate := fmt.Sprintf("client-%d", i)
		if candidate != id && s.shard(candidate) == want {
			return candidate
		}
	}
}

func shardID(s *memoryStore, shard int) string {
	for i := 0; ; i++ {
		candidate := fmt.Sprintf("shard-%d-client-%d", shard, i)
		if s.shard(candidate) == &s.shards[shard] {
			return candidate
		}
	}
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
		// The next call of shard 1 sweeps shard 0.
		s.shards[1].calls = memorySweepEvery - 1
		s.shards[1].sweepNext = 0
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
		hot := ids[memoryStoreShards-1]
		for range memoryStoreShards * memorySweepEvery {
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
		hot := ids[memoryStoreShards-1]
		var failures atomic.Int64
		var wg sync.WaitGroup
		for range memoryStoreShards * memorySweepEvery * 2 {
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
	if got := s.maxEntries; got != DefaultRateLimitMaxEntries {
		t.Errorf("max entries = %d, want %d", got, DefaultRateLimitMaxEntries)
	}
}

func TestMemoryStoreConfigDefaults(t *testing.T) {
	s := newTestMemoryStoreWithConfig(t, RateLimitMemoryStoreConfig{Rate: 2})
	if s.burst != 1 {
		t.Errorf("burst = %v, want 1", s.burst)
	}
	if s.maxAge != DefaultRateLimitMaxAge {
		t.Errorf("expiry = %s, want %s", s.maxAge, DefaultRateLimitMaxAge)
	}
	if s.maxEntries != DefaultRateLimitMaxEntries {
		t.Errorf("max entries = %d, want %d", s.maxEntries, DefaultRateLimitMaxEntries)
	}
	if !take(t, s, "ada") || take(t, s, "ada") {
		t.Error("the default burst did not allow exactly one immediate request")
	}
}

func TestMemoryStoreRejectsNegativeCapacity(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewRateLimitMemoryStoreWithConfig accepted a negative capacity")
		}
	}()
	newTestMemoryStoreWithConfig(t, RateLimitMemoryStoreConfig{Rate: 1, MaxEntries: -1})
}

func TestMemoryStoreCapacityDropsTheFirstToExpire(t *testing.T) {
	// Two clients a shard, and every client below lands in shard 0.
	s := newTestMemoryStoreWithConfig(t, RateLimitMemoryStoreConfig{
		Rate:       1,
		Burst:      2,
		MaxAge:     time.Hour,
		MaxEntries: 2 * memoryStoreShards,
	})
	ada := shardID(s, 0)
	grace := sameShardID(s, ada, 0)
	linus := ada
	for i := 0; linus == ada || linus == grace; i++ {
		linus = sameShardID(s, ada, i)
	}
	other := shardID(s, 1)
	if !take(t, s, other) || !take(t, s, ada) || !take(t, s, grace) {
		t.Fatal("the store denied an entry before reaching capacity")
	}
	// ada expires first in her shard, so the new client takes her place.
	if !take(t, s, linus) {
		t.Fatal("a new client was refused at capacity")
	}
	if !take(t, s, grace) {
		t.Error("a tracked client lost its remaining token at capacity")
	}
	if take(t, s, grace) {
		t.Error("a tracked client exceeded its burst at capacity")
	}
	if !take(t, s, other) || take(t, s, other) {
		t.Error("a full shard touched the client of another shard")
	}
	if got := memoryStoreSize(s); got != 3 {
		t.Errorf("the store holds %d buckets, want 3", got)
	}
}

func TestMemoryStoreSharesMaxEntriesAcrossShards(t *testing.T) {
	for _, maxEntries := range []int{1, 2, 31, 32, 33, 127, DefaultRateLimitMaxEntries} {
		t.Run(strconv.Itoa(maxEntries), func(t *testing.T) {
			s := newTestMemoryStoreWithConfig(t, RateLimitMemoryStoreConfig{Rate: 1, MaxEntries: maxEntries})
			total := 0
			for i := range s.nshards {
				c := s.shards[i].capacity
				if c < 1 {
					t.Errorf("shard %d holds %d clients, want one at least", i, c)
				}
				total += c
			}
			if total != maxEntries {
				t.Errorf("the shards hold %d clients together, want %d", total, maxEntries)
			}
			if want := uint64(min(maxEntries, memoryStoreShards)); s.nshards != want {
				t.Errorf("shards = %d, want %d", s.nshards, want)
			}
		})
	}
}

func TestMemoryStoreCapacityRecoversAfterExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestMemoryStoreWithConfig(t, RateLimitMemoryStoreConfig{
			Rate:       1000,
			Burst:      1,
			MaxAge:     time.Second,
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
	})
}

func TestMemoryStoreCapacityIsRaceSafe(t *testing.T) {
	const (
		maxEntries = 127
		requests   = 2048
	)
	s := newTestMemoryStoreWithConfig(t, RateLimitMemoryStoreConfig{
		Rate:       1,
		Burst:      1,
		MaxAge:     time.Hour,
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
	if got := allowed.Load(); got != requests {
		t.Errorf("allowed requests = %d, want %d: a full store makes room", got, requests)
	}
	if got := memoryStoreSize(s); got != maxEntries {
		t.Errorf("the store holds %d buckets, want %d", got, maxEntries)
	}
}

func TestMemoryStoreCardinalityStaysBoundedForUniqueIDs(t *testing.T) {
	const (
		maxEntries = 256
		requests   = 100000
	)
	s := newTestMemoryStoreWithConfig(t, RateLimitMemoryStoreConfig{
		Rate:       1,
		Burst:      1,
		MaxAge:     time.Hour,
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
	if allowed != requests {
		t.Errorf("allowed requests = %d, want %d: a full store makes room", allowed, requests)
	}
	if got := memoryStoreSize(s); got != maxEntries {
		t.Errorf("the store holds %d buckets, want %d", got, maxEntries)
	}
	if got := memoryStoreExpirySize(s); got != maxEntries {
		t.Errorf("the expiry heaps hold %d buckets, want %d", got, maxEntries)
	}
}

func BenchmarkMemoryStoreParallel(b *testing.B) {
	const clients = 1024
	benchmarks := []struct {
		name       string
		maxEntries int
		unique     bool
	}{
		{"returning clients", 0, false},
		{"new clients in a full store", clients, true},
	}
	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			s := NewRateLimitMemoryStoreWithConfig(RateLimitMemoryStoreConfig{
				Rate:       1e9,
				Burst:      1,
				MaxAge:     time.Hour,
				MaxEntries: bm.maxEntries,
			})
			ids := make([]string, clients)
			for i := range ids {
				ids[i] = strconv.Itoa(i)
				_, _, _ = s.Allow(nil, ids[i])
			}
			var worker atomic.Int64
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				prefix := strconv.FormatInt(worker.Add(1), 10) + "-"
				for i := 0; pb.Next(); i++ {
					id := ids[i%clients]
					if bm.unique {
						id = prefix + strconv.Itoa(i)
					}
					if _, _, err := s.Allow(nil, id); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

func BenchmarkMemoryStoreAtCapacityUniqueIDs(b *testing.B) {
	const maxEntries = 1024
	s := NewRateLimitMemoryStoreWithConfig(RateLimitMemoryStoreConfig{
		Rate:       1,
		Burst:      1,
		MaxAge:     time.Hour,
		MaxEntries: maxEntries,
	}).(*memoryStore)
	for i := range maxEntries {
		if allowed, _, err := s.Allow(nil, strconv.Itoa(i)); err != nil || !allowed {
			b.Fatalf("fill request %d: allowed = %t, err = %v", i, allowed, err)
		}
	}
	b.ReportAllocs()
	// A full shard evicts the entry that expires first, so each new client is
	// admitted and the store never passes its bound. Each shard caps at its own
	// share, and the keys spread unevenly, so the store can hold fewer than
	// maxEntries until every shard has filled.
	for i := 0; b.Loop(); i++ {
		allowed, _, err := s.Allow(nil, strconv.Itoa(i+maxEntries))
		if err != nil || !allowed {
			b.Fatalf("request %d: allowed = %t, err = %v", i, allowed, err)
		}
	}
	if got := memoryStoreSize(s); got > maxEntries {
		b.Fatalf("the store holds %d buckets, want at most %d", got, maxEntries)
	}
}
