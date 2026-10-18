package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"github.com/dmitrymomot/go-router/cookie"
)

// ErrNotFound is what a [Store] reports for a token that names no live
// session: one it never saved, deleted, or let run out.
var ErrNotFound = errors.New("session: no such session")

// Store keeps the session records. A token is what the cookie carries.
//
// The middleware makes the token of a new session with [NewToken] and passes
// it to Save. A store on the server, such as a table of the application,
// keys the record by the token and reports it back as it stands. A store that
// keeps the record in the cookie itself, such as [CookieStore], reports the
// sealed record as the token.
//
// Load reports the data that Save stored for token, and [ErrNotFound] for a
// token that names no session or one whose expiry passed. Save stores data
// under token until expires, in place of what the token held. Delete removes
// the record, so the next Load of the token reports ErrNotFound; a token that
// names nothing is no error.
//
// Any other error of a Store goes to the handler, or answers the request with
// a 500 when the middleware meets it. A Store must be safe for concurrent use.
type Store interface {
	Load(ctx context.Context, token string) ([]byte, error)
	Save(ctx context.Context, token string, data []byte, expires time.Time) (string, error)
	Delete(ctx context.Context, token string) error
}

// NewToken reports a token for a new session: 128 random bits from
// [rand.Text], which a cookie carries as it stands.
func NewToken() string {
	return rand.Text()
}

// sweepEvery is how many saves a MemoryStore makes between two passes that
// drop the records that ran out.
const sweepEvery = 1024

// MemoryStore keeps the records in the memory of the process. It suits tests
// and a program of one process: a restart signs everyone out, and a second
// process does not see the sessions of the first. Build one with
// [NewMemoryStore].
//
// Delete revokes a session at once, since the middleware loads the record on
// every request.
type MemoryStore struct {
	records map[string]memoryRecord
	mu      sync.Mutex
	saves   int
}

type memoryRecord struct {
	expires time.Time
	data    []byte
}

// NewMemoryStore builds an empty [MemoryStore].
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[string]memoryRecord)}
}

// Load implements [Store].
func (m *MemoryStore) Load(_ context.Context, token string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[token]
	if !ok {
		return nil, ErrNotFound
	}
	if !time.Now().Before(r.expires) {
		delete(m.records, token)
		return nil, ErrNotFound
	}
	return bytes.Clone(r.data), nil
}

// Save implements [Store]. It reports token as it stands.
func (m *MemoryStore) Save(_ context.Context, token string, data []byte, expires time.Time) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[token] = memoryRecord{expires: expires, data: bytes.Clone(data)}
	if m.saves++; m.saves >= sweepEvery {
		m.saves = 0
		now := time.Now()
		for t, r := range m.records {
			if !now.Before(r.expires) {
				delete(m.records, t)
			}
		}
	}
	return token, nil
}

// Delete implements [Store].
func (m *MemoryStore) Delete(_ context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, token)
	return nil
}

// Len reports how many records the store holds, including any that ran out
// and were not dropped yet.
func (m *MemoryStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.records)
}

// cookiePurpose is the name a CookieStore binds a sealed record to, so a value
// that the same codec signed for another cookie does not open as a session.
const cookiePurpose = "session"

// CookieStore keeps the whole record in the cookie, sealed by a
// [cookie.Codec], so the server stores nothing. Build one with
// [NewCookieStore].
//
// It cannot revoke a session: Delete does nothing, and a copy of the cookie
// stays valid until it runs out, after a logout too. Keep the Lifetime short,
// or use a store on the server where a logout must end the session at once.
// The record and the cookie together must fit in [cookie.MaxSize] bytes; a
// [Put] of a larger value reports [cookie.ErrTooLarge].
type CookieStore struct {
	codec *cookie.Codec
}

// NewCookieStore builds a [CookieStore] that seals with codec. A codec from
// [cookie.NewEncryptedCodec] hides the data from the client; one from
// [cookie.NewCodec] only signs it, and the client reads it.
//
// NewCookieStore panics if codec is nil.
func NewCookieStore(codec *cookie.Codec) *CookieStore {
	if codec == nil {
		panic("session: NewCookieStore needs a codec")
	}
	return &CookieStore{codec: codec}
}

// Load implements [Store]. A token that does not open, or that ran out,
// reports [ErrNotFound].
func (s *CookieStore) Load(_ context.Context, token string) ([]byte, error) {
	data, err := s.codec.Decode(cookiePurpose, token)
	if err != nil {
		return nil, ErrNotFound
	}
	return data, nil
}

// Save implements [Store]. It reports the sealed record as the token.
func (s *CookieStore) Save(_ context.Context, _ string, data []byte, expires time.Time) (string, error) {
	return s.codec.Encode(cookiePurpose, data, expires), nil
}

// Delete implements [Store]. It does nothing: the record lives in the cookie.
func (s *CookieStore) Delete(context.Context, string) error { return nil }
