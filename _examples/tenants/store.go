package main

import (
	"cmp"
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
	Owner   string
}

var (
	ErrSlugEmpty    = errors.New("the name has no letters or digits to build a subdomain from")
	ErrSlugReserved = errors.New("that subdomain is reserved")
	ErrSlugTaken    = errors.New("that subdomain is taken")
)

// reserved names never become a workspace, because the apex router already
// answers on them.
var reserved = []string{"www", "api", "admin", "static", "mail"}

type Store struct {
	bySlug map[string]Workspace
	mu     sync.RWMutex
}

func NewStore() *Store { return &Store{bySlug: make(map[string]Workspace)} }

func (s *Store) Create(name, owner string) (Workspace, error) {
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
	w := Workspace{Slug: slug, Name: name, Owner: owner, Created: time.Now()}
	s.bySlug[slug] = w
	return w, nil
}

func (s *Store) Get(slug string) (Workspace, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	w, ok := s.bySlug[slug]
	return w, ok
}

func (s *Store) OwnedBy(owner string) []Workspace {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []Workspace
	for _, w := range s.bySlug {
		if w.Owner == owner {
			out = append(out, w)
		}
	}
	slices.SortFunc(out, func(a, b Workspace) int { return cmp.Compare(a.Slug, b.Slug) })
	return out
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
