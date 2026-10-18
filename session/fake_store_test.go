package session_test

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/dmitrymomot/go-router/session"
)

// fakeStore is a session.Store for tests only: the records live in a map and
// go away with the test. It honors the expiry as a database query would.
type fakeStore struct {
	records map[string]fakeRecord
	mu      sync.Mutex
}

type fakeRecord struct {
	expires time.Time
	data    []byte
}

var _ session.Store = (*fakeStore)(nil)

func newFakeStore() *fakeStore {
	return &fakeStore{records: make(map[string]fakeRecord)}
}

func (s *fakeStore) Load(_ context.Context, token string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[token]
	if !ok || !time.Now().Before(r.expires) {
		return nil, session.ErrNotFound
	}
	return bytes.Clone(r.data), nil
}

func (s *fakeStore) Save(_ context.Context, token string, data []byte, expires time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[token] = fakeRecord{expires: expires, data: bytes.Clone(data)}
	return token, nil
}

func (s *fakeStore) Delete(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, token)
	return nil
}

// Len reports how many records the store holds.
func (s *fakeStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}
