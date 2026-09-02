package main

import (
	"cmp"
	"slices"
	"sync"
)

type User struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	ID    int    `json:"id"`
}

// Store keeps the users in memory. A real service holds a database here.
type Store struct {
	users map[int]User
	next  int
	mu    sync.RWMutex
}

func NewStore() *Store { return &Store{users: make(map[int]User)} }

func (s *Store) List() []User {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]User, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, u)
	}
	slices.SortFunc(out, func(a, b User) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func (s *Store) Get(id int) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	u, ok := s.users[id]
	return u, ok
}

func (s *Store) Create(name, email string) User {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.next++
	u := User{ID: s.next, Name: name, Email: email}
	s.users[u.ID] = u
	return u
}

func (s *Store) Update(id int, name, email string) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.users[id]; !ok {
		return User{}, false
	}
	u := User{ID: id, Name: name, Email: email}
	s.users[id] = u
	return u, true
}

func (s *Store) Delete(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.users[id]; !ok {
		return false
	}
	delete(s.users, id)
	return true
}
