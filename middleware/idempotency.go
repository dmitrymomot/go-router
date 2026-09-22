package middleware

import (
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/dmitrymomot/go-router"
)

// The defaults of [IdempotencyWithConfig] and of the memory store.
const (
	// DefaultIdempotencyFormField is the form field that carries the key of a
	// plain HTML form.
	DefaultIdempotencyFormField = "_idempotency_key"
	// DefaultIdempotencyMaxBody is the longest answer that is kept for a
	// replay.
	DefaultIdempotencyMaxBody = 64 << 10
	// DefaultIdempotencyWait is how long a repeat waits for the request that
	// holds its key.
	DefaultIdempotencyWait = 10 * time.Second
	// DefaultIdempotencyExpiry is how long the memory store keeps a key.
	DefaultIdempotencyExpiry = 24 * time.Hour
	// DefaultIdempotencyMaxEntries is how many keys the memory store holds.
	DefaultIdempotencyMaxEntries = 1024
	// MaxIdempotencyKeyLength is the longest key a client may send.
	MaxIdempotencyKeyLength = 255
)

// The causes of the answers of [IdempotencyWithConfig]. Match them with
// [errors.Is] in an error handler.
var (
	ErrIdempotencyKeyRequired = errors.New("middleware: the request needs an idempotency key")
	ErrIdempotencyKeyReused   = errors.New("middleware: the idempotency key was used for a different request")
	ErrIdempotencyInProgress  = errors.New("middleware: a request with this idempotency key is still running")
	ErrIdempotencyTooLarge    = errors.New("middleware: the answer to this idempotency key was too large to keep")
)

var errIdempotencyStoreFull = errors.New("middleware: every idempotency key in the store is still running")

// IdempotencyEntry is what an [IdempotencyStore] holds for one key.
//
//betteralign:check
type IdempotencyEntry struct {
	// Fingerprint is the fingerprint of the request that claimed the key. The
	// caller must not change it.
	Fingerprint []byte
	// Answer is nil while that request still runs.
	Answer *router.Recorded
}

// IdempotencyStore holds the keys of [IdempotencyWithConfig].
//
// Claim is atomic. With no live entry for key, it stores a running entry that
// holds fp and reports claimed. Otherwise it reports the entry it holds. The
// store never compares fingerprints; the middleware does.
//
// Complete stores the answer and Release deletes the entry, both only while
// the entry is still running. They run after the handler, when c may already
// be done, so a store that goes over the network uses context.WithoutCancel(c).
// Keep every field of the [router.Recorded], Truncated included.
//
// An error from Claim answers 500. An error from Complete or Release is
// logged, and the key stays held until it expires.
//
// [NewIdempotencyMemoryStore] holds the keys in this process. Implement the
// interface to share them across several, such as through Redis.
type IdempotencyStore[C router.Context] interface {
	Claim(c C, key string, fp []byte) (held IdempotencyEntry, claimed bool, err error)
	Complete(c C, key string, rec router.Recorded) error
	Release(c C, key string) error
}

// IdempotencyMemoryStoreConfig configures [NewIdempotencyMemoryStoreWithConfig].
// ExpiresIn is how long a key lives, counted from its claim, and zero or less
// takes [DefaultIdempotencyExpiry]. MaxEntries caps how many keys the store
// holds, and zero takes [DefaultIdempotencyMaxEntries].
type IdempotencyMemoryStoreConfig struct {
	ExpiresIn  time.Duration
	MaxEntries int
}

// NewIdempotencyMemoryStore builds an [IdempotencyStore] in this process that
// keeps a key for expiresIn after its claim. An expiresIn of zero or less takes
// [DefaultIdempotencyExpiry].
//
// The keys live in one process, so several instances each hold their own.
func NewIdempotencyMemoryStore[C router.Context](expiresIn time.Duration) IdempotencyStore[C] {
	return NewIdempotencyMemoryStoreWithConfig[C](IdempotencyMemoryStoreConfig{ExpiresIn: expiresIn})
}

// NewIdempotencyMemoryStoreWithConfig is [NewIdempotencyMemoryStore] with a
// configuration, which also caps how many keys the store holds.
//
// A full store makes room by dropping the oldest key whose answer is stored. A
// store full of running keys refuses a new one, and the request gets a 500. It
// holds up to MaxEntries answers of up to the MaxBody of the middleware each.
//
// A key lives for ExpiresIn from its claim, however long its request runs. A
// request that outlives it can store its answer on, or release, the claim of a
// later request with the same key.
//
// NewIdempotencyMemoryStoreWithConfig panics on a negative MaxEntries.
func NewIdempotencyMemoryStoreWithConfig[C router.Context](cfg IdempotencyMemoryStoreConfig) IdempotencyStore[C] {
	if cfg.MaxEntries < 0 {
		panic("middleware: NewIdempotencyMemoryStoreWithConfig needs a MaxEntries of zero or more")
	}
	if cfg.ExpiresIn <= 0 {
		cfg.ExpiresIn = DefaultIdempotencyExpiry
	}
	if cfg.MaxEntries == 0 {
		cfg.MaxEntries = DefaultIdempotencyMaxEntries
	}
	return &idempotencyMemoryStore[C]{
		entries:    make(map[string]*idempotencyMemoryEntry),
		expiresIn:  cfg.ExpiresIn,
		maxEntries: cfg.MaxEntries,
	}
}

// The entries form a list in the order of their claims. Every entry lives for
// the same time from its claim, so that is also the order they expire in.
type idempotencyMemoryStore[C router.Context] struct {
	entries    map[string]*idempotencyMemoryEntry
	head, tail *idempotencyMemoryEntry
	expiresIn  time.Duration
	maxEntries int
	mu         sync.Mutex
}

type idempotencyMemoryEntry struct {
	expires    time.Time
	answer     *router.Recorded
	prev, next *idempotencyMemoryEntry
	key        string
	fp         []byte
}

func (s *idempotencyMemoryStore[C]) Claim(_ C, key string, fp []byte) (IdempotencyEntry, bool, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	for s.head != nil && !now.Before(s.head.expires) {
		s.remove(s.head)
	}
	if e, ok := s.entries[key]; ok {
		return IdempotencyEntry{Fingerprint: e.fp, Answer: cloneRecorded(e.answer)}, false, nil
	}
	if len(s.entries) >= s.maxEntries {
		// A running entry stays: dropping it would let its key run twice.
		victim := s.head
		for victim != nil && victim.answer == nil {
			victim = victim.next
		}
		if victim == nil {
			return IdempotencyEntry{}, false, errIdempotencyStoreFull
		}
		s.remove(victim)
	}

	e := &idempotencyMemoryEntry{key: key, fp: slices.Clone(fp), expires: now.Add(s.expiresIn), prev: s.tail}
	if s.tail != nil {
		s.tail.next = e
	} else {
		s.head = e
	}
	s.tail = e
	s.entries[key] = e
	return IdempotencyEntry{Fingerprint: e.fp}, true, nil
}

func (s *idempotencyMemoryStore[C]) Complete(_ C, key string, rec router.Recorded) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok && e.answer == nil {
		e.answer = cloneRecorded(&rec)
	}
	return nil
}

func (s *idempotencyMemoryStore[C]) Release(_ C, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok && e.answer == nil {
		s.remove(e)
	}
	return nil
}

func (s *idempotencyMemoryStore[C]) remove(e *idempotencyMemoryEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		s.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		s.tail = e.prev
	}
	e.prev, e.next = nil, nil
	delete(s.entries, e.key)
}

func cloneRecorded(rec *router.Recorded) *router.Recorded {
	if rec == nil {
		return nil
	}
	out := *rec
	out.Header = rec.Header.Clone()
	out.Body = slices.Clone(rec.Body)
	return &out
}
