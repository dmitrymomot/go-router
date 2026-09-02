package main

import (
	"crypto/rand"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// A workspace lives at its own subdomain, and the slug is that subdomain.
type Workspace struct {
	Created time.Time
	Slug    string
	Name    string
}

var (
	ErrSlugEmpty    = errors.New("the name has no letters or digits to build a subdomain from")
	ErrSlugReserved = errors.New("that subdomain is reserved")
	ErrSlugTaken    = errors.New("that subdomain is taken")

	ErrEmailTaken = errors.New("that email already has an account here")
	// ErrBadCredentials never says which half was wrong, so the form cannot be
	// used to learn which addresses have an account.
	ErrBadCredentials = errors.New("that email and password do not match")
)

// An account belongs to one workspace. The same address may hold an account in
// two of them, and they are two accounts: a password changed in one leaves the
// other alone. The password is kept as a salt and a derived key, never as
// itself.
type Account struct {
	Workspace string
	Email     string
	Salt      []byte
	Key       []byte
}

// A ticket carries a new owner from the apex, where the workspace was made, to
// the workspace host, which is the only place that can set its session cookie.
type ticket struct {
	expires   time.Time
	workspace string
	email     string
}

const ticketMaxAge = time.Minute

// reserved names never become a workspace, because the apex router already
// answers on them.
var reserved = []string{"www", "api", "admin", "static", "mail"}

type Store struct {
	bySlug   map[string]Workspace
	accounts map[string]Account
	tickets  map[string]ticket
	mu       sync.RWMutex
}

func NewStore() *Store {
	return &Store{
		bySlug:   make(map[string]Workspace),
		accounts: make(map[string]Account),
		tickets:  make(map[string]ticket),
	}
}

// accountKey names one account inside one workspace.
func accountKey(slug, email string) string { return slug + "\x00" + email }

func (s *Store) Create(name string) (Workspace, error) {
	slug := slugify(name)
	switch {
	case slug == "":
		return Workspace{}, ErrSlugEmpty
	case slices.Contains(reserved, slug):
		return Workspace{}, ErrSlugReserved
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, taken := s.bySlug[slug]; taken {
		return Workspace{}, ErrSlugTaken
	}
	w := Workspace{Slug: slug, Name: name, Created: time.Now()}
	s.bySlug[slug] = w
	return w, nil
}

func (s *Store) Get(slug string) (Workspace, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	w, ok := s.bySlug[slug]
	return w, ok
}

func (s *Store) Register(slug, email, password string) error {
	salt := newSalt()
	key := derive(password, salt)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, taken := s.accounts[accountKey(slug, email)]; taken {
		return ErrEmailTaken
	}
	s.accounts[accountKey(slug, email)] = Account{
		Workspace: slug, Email: email, Salt: salt, Key: key,
	}
	return nil
}

func (s *Store) Authenticate(slug, email, password string) error {
	s.mu.RLock()
	a, ok := s.accounts[accountKey(slug, email)]
	s.mu.RUnlock()

	if !ok {
		// Derive anyway. An address with no account here must not answer
		// faster than one with a wrong password.
		derive(password, make([]byte, saltLen))
		return ErrBadCredentials
	}
	if !passwordMatches(password, a.Salt, a.Key) {
		return ErrBadCredentials
	}
	return nil
}

func (s *Store) NewTicket(slug, email string) string {
	id := rand.Text()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.tickets[id] = ticket{workspace: slug, email: email, expires: time.Now().Add(ticketMaxAge)}
	return id
}

// Redeem spends a ticket. It works once, and only inside its minute.
func (s *Store) Redeem(id string) (ticket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.tickets[id]
	if !ok {
		return ticket{}, false
	}
	delete(s.tickets, id)
	return t, time.Now().Before(t.expires)
}

// slugify turns "Acme, Inc." into "acme-inc". A subdomain is one DNS label, so
// it holds letters, digits and inner hyphens, and nothing else.
func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case unicode.IsLetter(r) && r < unicode.MaxASCII || unicode.IsDigit(r) && r < unicode.MaxASCII:
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	slug := b.String()
	if len(slug) > maxSlugLen {
		slug = strings.TrimRight(slug[:maxSlugLen], "-")
	}
	return slug
}

const maxSlugLen = 30
