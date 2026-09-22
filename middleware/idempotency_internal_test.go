package middleware

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router"
)

func newTestIdempotencyStore(t *testing.T, cfg IdempotencyMemoryStoreConfig) *idempotencyMemoryStore[router.Context] {
	t.Helper()
	s, ok := NewIdempotencyMemoryStoreWithConfig[router.Context](cfg).(*idempotencyMemoryStore[router.Context])
	if !ok {
		t.Fatal("NewIdempotencyMemoryStoreWithConfig no longer returns an *idempotencyMemoryStore")
	}
	return s
}

func mustClaim(t *testing.T, s *idempotencyMemoryStore[router.Context], key, fp string) (IdempotencyEntry, bool) {
	t.Helper()
	held, claimed, err := s.Claim(nil, key, []byte(fp))
	if err != nil {
		t.Fatalf("Claim(%q): %v", key, err)
	}
	return held, claimed
}

// storeKeys walks the list both ways and checks it against the map.
func storeKeys(t *testing.T, s *idempotencyMemoryStore[router.Context]) []string {
	t.Helper()
	var keys []string
	var prev *idempotencyMemoryEntry
	for e := s.head; e != nil; e = e.next {
		if e.prev != prev {
			t.Fatalf("entry %q points back at the wrong entry", e.key)
		}
		if s.entries[e.key] != e {
			t.Fatalf("entry %q is on the list but not in the map", e.key)
		}
		keys = append(keys, e.key)
		prev = e
	}
	if s.tail != prev {
		t.Fatal("the tail is not the last entry")
	}
	if len(keys) != len(s.entries) {
		t.Fatalf("the list holds %d entries and the map %d", len(keys), len(s.entries))
	}
	return keys
}

func TestIdempotencyMemoryStoreDefaults(t *testing.T) {
	s := newTestIdempotencyStore(t, IdempotencyMemoryStoreConfig{})
	if s.expiresIn != DefaultIdempotencyExpiry || s.maxEntries != DefaultIdempotencyMaxEntries {
		t.Errorf("defaults = %v/%d, want %v/%d", s.expiresIn, s.maxEntries,
			DefaultIdempotencyExpiry, DefaultIdempotencyMaxEntries)
	}
	if s, ok := NewIdempotencyMemoryStore[router.Context](-time.Second).(*idempotencyMemoryStore[router.Context]); !ok ||
		s.expiresIn != DefaultIdempotencyExpiry {
		t.Error("a negative expiry did not take the default")
	}
	mustPanicWith(t, "MaxEntries", func() {
		NewIdempotencyMemoryStoreWithConfig[router.Context](IdempotencyMemoryStoreConfig{MaxEntries: -1})
	})
}

func mustPanicWith(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		if msg, _ := recover().(string); !strings.Contains(msg, want) {
			t.Errorf("panic = %q, want one that holds %q", msg, want)
		}
	}()
	fn()
}

func TestIdempotencyMemoryStoreHandsBackWhatItHolds(t *testing.T) {
	s := newTestIdempotencyStore(t, IdempotencyMemoryStoreConfig{})
	if _, claimed := mustClaim(t, s, "k", "fp-a"); !claimed {
		t.Fatal("the first Claim did not claim")
	}
	held, claimed := mustClaim(t, s, "k", "fp-b")
	if claimed || string(held.Fingerprint) != "fp-a" || held.Answer != nil {
		t.Fatalf("running: claimed=%v fp=%q answer=%v, want false fp-a nil", claimed, held.Fingerprint, held.Answer)
	}

	rec := router.Recorded{Status: http.StatusCreated, Header: http.Header{"X-A": {"1"}}, Body: []byte("made"), Truncated: true}
	if err := s.Complete(nil, "k", rec); err != nil {
		t.Fatal(err)
	}
	rec.Body[0] = 'X'
	rec.Header.Set("X-A", "changed")

	held, _ = mustClaim(t, s, "k", "fp-a")
	got := held.Answer
	if got == nil || got.Status != http.StatusCreated || string(got.Body) != "made" ||
		got.Header.Get("X-A") != "1" || !got.Truncated {
		t.Fatalf("answer = %+v, want the one stored, unchanged by the caller", got)
	}
	got.Body[0] = 'Y'
	if held, _ = mustClaim(t, s, "k", "fp-a"); string(held.Answer.Body) != "made" {
		t.Error("changing the answer a Claim handed back changed the store")
	}
}

func TestIdempotencyMemoryStoreCompletesAndReleasesOnlyARunningEntry(t *testing.T) {
	s := newTestIdempotencyStore(t, IdempotencyMemoryStoreConfig{})

	mustClaim(t, s, "done", "fp")
	if err := s.Complete(nil, "done", router.Recorded{Status: http.StatusOK, Body: []byte("first")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(nil, "done", router.Recorded{Status: http.StatusTeapot}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(nil, "done"); err != nil {
		t.Fatal(err)
	}
	if held, claimed := mustClaim(t, s, "done", "fp"); claimed || held.Answer == nil || held.Answer.Status != http.StatusOK {
		t.Errorf("a finished entry changed: claimed=%v answer=%+v", claimed, held.Answer)
	}

	mustClaim(t, s, "gone", "fp")
	if err := s.Release(nil, "gone"); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(nil, "gone", router.Recorded{Status: http.StatusOK}); err != nil {
		t.Fatal(err)
	}
	if _, claimed := mustClaim(t, s, "gone", "fp"); !claimed {
		t.Error("a Complete after the Release brought the entry back")
	}

	if err := s.Complete(nil, "never", router.Recorded{}); err != nil {
		t.Error(err)
	}
	if err := s.Release(nil, "never"); err != nil {
		t.Error(err)
	}
}

func TestIdempotencyMemoryStoreKeepsItsListWhole(t *testing.T) {
	for _, drop := range []string{"a", "b", "c"} {
		t.Run(drop, func(t *testing.T) {
			s := newTestIdempotencyStore(t, IdempotencyMemoryStoreConfig{})
			for _, k := range []string{"a", "b", "c"} {
				mustClaim(t, s, k, "fp")
			}
			if err := s.Release(nil, drop); err != nil {
				t.Fatal(err)
			}
			want := slices.DeleteFunc([]string{"a", "b", "c"}, func(k string) bool { return k == drop })
			if got := storeKeys(t, s); !slices.Equal(got, want) {
				t.Errorf("keys = %v, want %v", got, want)
			}
			mustClaim(t, s, "d", "fp")
			if got := storeKeys(t, s); !slices.Equal(got, append(want, "d")) {
				t.Errorf("keys = %v after a new claim, want %v", got, append(want, "d"))
			}
		})
	}
}

// The expiry counts from the claim, and a Complete does not move it, so the
// list stays in the order the entries expire in.
func TestIdempotencyMemoryStoreExpiresFromTheClaim(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestIdempotencyStore(t, IdempotencyMemoryStoreConfig{ExpiresIn: time.Minute})
		mustClaim(t, s, "a", "fp")
		time.Sleep(30 * time.Second)
		mustClaim(t, s, "b", "fp")
		if err := s.Complete(nil, "a", router.Recorded{Status: http.StatusOK}); err != nil {
			t.Fatal(err)
		}

		time.Sleep(30 * time.Second)
		if _, claimed := mustClaim(t, s, "a", "fp"); !claimed {
			t.Error("a key outlived its expiry because a Complete refreshed it")
		}
		if got := storeKeys(t, s); !slices.Equal(got, []string{"b", "a"}) {
			t.Errorf("keys = %v, want [b a]", got)
		}

		time.Sleep(30 * time.Second)
		mustClaim(t, s, "c", "fp")
		if got := storeKeys(t, s); !slices.Equal(got, []string{"a", "c"}) {
			t.Errorf("keys = %v, want [a c]", got)
		}
	})
}

func TestIdempotencyMemoryStoreEvictsTheOldestFinishedEntry(t *testing.T) {
	s := newTestIdempotencyStore(t, IdempotencyMemoryStoreConfig{MaxEntries: 3})
	for _, k := range []string{"running", "done-1", "done-2"} {
		mustClaim(t, s, k, "fp")
	}
	for _, k := range []string{"done-1", "done-2"} {
		if err := s.Complete(nil, k, router.Recorded{Status: http.StatusOK}); err != nil {
			t.Fatal(err)
		}
	}

	mustClaim(t, s, "new", "fp")
	if got := storeKeys(t, s); !slices.Equal(got, []string{"running", "done-2", "new"}) {
		t.Errorf("keys = %v, want the running entry kept and done-1 dropped", got)
	}
}

func TestIdempotencyMemoryStoreRefusesWhenEveryEntryRuns(t *testing.T) {
	s := newTestIdempotencyStore(t, IdempotencyMemoryStoreConfig{MaxEntries: 2})
	mustClaim(t, s, "a", "fp")
	mustClaim(t, s, "b", "fp")
	if _, claimed, err := s.Claim(nil, "c", []byte("fp")); !errors.Is(err, errIdempotencyStoreFull) || claimed {
		t.Errorf("Claim on a full store = %v, %v; want errIdempotencyStoreFull", claimed, err)
	}
	if held, claimed := mustClaim(t, s, "a", "fp"); claimed || string(held.Fingerprint) != "fp" {
		t.Error("a full store turned away a key it holds")
	}
}

func TestIdempotencyStoreKeyKeepsEveryPairApart(t *testing.T) {
	pairs := [][2]string{
		{"", "3:abcX"},
		{"abc", "X"},
		{"ab", "cX"},
		{"a", "bcX"},
		{"1:a", "b"},
		{"1", ":ab"},
	}
	seen := map[string][2]string{}
	for _, p := range pairs {
		k := idempotencyStoreKey(p[0], p[1])
		if other, ok := seen[k]; ok {
			t.Errorf("scope %q key %q and scope %q key %q share the store key %q", p[0], p[1], other[0], other[1], k)
		}
		seen[k] = p
	}
}

func TestIdempotencyHeaderDeltaKeepsWhatTheHandlerAdded(t *testing.T) {
	before := http.Header{
		"X-Request-Id": {"r1"},
		"Vary":         {"Accept-Encoding"},
		"Set-Cookie":   {"_csrf=t"},
	}
	after := http.Header{
		"X-Request-Id": {"r1"},
		"Vary":         {"Accept-Encoding", "Cookie"},
		"Set-Cookie":   {"_csrf=t", "receipt=r1"},
		"X-Charge":     {"ch_1"},
	}
	got := idempotencyHeaderDelta(before, after)
	want := http.Header{
		"Vary":       {"Accept-Encoding", "Cookie"},
		"Set-Cookie": {"receipt=r1"},
		"X-Charge":   {"ch_1"},
	}
	if len(got) != len(want) {
		t.Fatalf("delta = %v, want %v", got, want)
	}
	for k, vs := range want {
		if !slices.Equal(got[k], vs) {
			t.Errorf("delta[%s] = %q, want %q", k, got[k], vs)
		}
	}

	after["X-Charge"][0] = "changed"
	if got.Get("X-Charge") != "ch_1" {
		t.Error("the delta shares its values with the header it came from")
	}
	if got := idempotencyHeaderDelta(before, before); len(got) != 0 {
		t.Errorf("delta of an unchanged header = %v, want none", got)
	}
}
