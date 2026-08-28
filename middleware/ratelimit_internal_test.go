package middleware

import (
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

func take(t *testing.T, s *memoryStore[router.Context], id string) bool {
	t.Helper()
	allowed, _, err := s.Allow(nil, id)
	if err != nil {
		t.Fatalf("Allow(%q): %v", id, err)
	}
	return allowed
}

func TestMemoryStoreSweepDropsARefilledBucket(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestMemoryStore(t, 2, 2, time.Second)

		take(t, s, "ada")
		take(t, s, "grace")
		if len(s.visitors) != 2 {
			t.Fatalf("the store holds %d buckets, want 2", len(s.visitors))
		}

		time.Sleep(2 * time.Second)
		take(t, s, "ada")

		if len(s.visitors) != 1 {
			t.Errorf("the store holds %d buckets, want 1: the sweep kept a bucket that had refilled", len(s.visitors))
		}
	})
}

func TestMemoryStoreSweepKeepsABucketThatOwesTokens(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestMemoryStore(t, 0.001, 2, time.Second)

		take(t, s, "ada")
		take(t, s, "ada")
		if take(t, s, "ada") {
			t.Fatal("the third request passed a burst of two")
		}

		time.Sleep(2 * time.Second)
		take(t, s, "grace")

		if len(s.visitors) != 2 {
			t.Fatalf("the store holds %d buckets, want 2: the sweep dropped a bucket that still owed tokens", len(s.visitors))
		}
		if take(t, s, "ada") {
			t.Error("the client passed on a bucket that the store started again, which is the burst it never earned")
		}
	})
}
