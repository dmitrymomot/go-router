package middleware

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router"
)

// newTestMemoryStore returns the store behind the interface, so that a test
// reads the map it keeps. That map is the only place the sweep shows: a bucket
// that refilled answers exactly as a bucket that the sweep dropped and Allow
// started again, which is what makes the eviction safe and invisible at once.
func newTestMemoryStore(t *testing.T, rate float64, burst int, expiresIn time.Duration) *memoryStore[router.Context] {
	t.Helper()
	s, ok := NewMemoryStore[router.Context](rate, burst, expiresIn).(*memoryStore[router.Context])
	if !ok {
		t.Fatal("NewMemoryStore no longer returns a *memoryStore")
	}
	return s
}

// take asks the store for one request and reports whether it passed.
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
		// Two a second and two at once, so a second of quiet refills the burst.
		s := newTestMemoryStore(t, 2, 2, time.Second)

		take(t, s, "ada")
		take(t, s, "grace")
		if len(s.visitors) != 2 {
			t.Fatalf("the store holds %d buckets, want 2", len(s.visitors))
		}

		time.Sleep(2 * time.Second)
		// The request sweeps first, then makes a bucket of its own.
		take(t, s, "ada")

		if len(s.visitors) != 1 {
			t.Errorf("the store holds %d buckets, want 1: the sweep kept a bucket that had refilled", len(s.visitors))
		}
	})
}

func TestMemoryStoreSweepKeepsABucketThatOwesTokens(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A thousandth of a request a second against a burst of two: the wait
		// that follows refills a five-hundredth of one token.
		s := newTestMemoryStore(t, 0.001, 2, time.Second)

		take(t, s, "ada")
		take(t, s, "ada")
		if take(t, s, "ada") {
			t.Fatal("the third request passed a burst of two")
		}

		time.Sleep(2 * time.Second)
		// The request of another identity is what runs the sweep.
		take(t, s, "grace")

		if len(s.visitors) != 2 {
			t.Fatalf("the store holds %d buckets, want 2: the sweep dropped a bucket that still owed tokens", len(s.visitors))
		}
		if take(t, s, "ada") {
			t.Error("the client passed on a bucket that the store started again, which is the burst it never earned")
		}
	})
}
