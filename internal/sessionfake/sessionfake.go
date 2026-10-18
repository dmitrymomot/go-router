// Package sessionfake holds the session.Store that the tests of this module
// run on. It keeps the records in a map, which no application should, so it
// lives under internal and no module outside this one can import it.
package sessionfake

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/dmitrymomot/go-router/session"
)

// Store is a session.Store in memory. It honors the expiry as a query on a
// table would. Build one with [New].
type Store struct {
	records map[string]record
	mu      sync.Mutex
}

type record struct {
	expires time.Time
	data    []byte
}

var _ session.Store = (*Store)(nil)

// New builds an empty [Store].
func New() *Store {
	return &Store{records: make(map[string]record)}
}

// Load implements session.Store.
func (s *Store) Load(_ context.Context, token string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[token]
	if !ok || !time.Now().Before(r.expires) {
		return nil, session.ErrNotFound
	}
	return bytes.Clone(r.data), nil
}

// Save implements session.Store. It reports token as it stands.
func (s *Store) Save(_ context.Context, token string, data []byte, expires time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[token] = record{expires: expires, data: bytes.Clone(data)}
	return token, nil
}

// Delete implements session.Store.
func (s *Store) Delete(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, token)
	return nil
}

// Len reports how many records the store holds, expired ones included.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}
