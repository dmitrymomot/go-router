package main

import (
	"strings"
	"sync"
	"time"
	"unicode"
)

type kind string

const (
	kindMessage kind = "message"
	kindNotice  kind = "notice"
)

// message is what the server sends: JSON, which the page renders with
// textContent, so no text of a user ever becomes HTML.
type message struct {
	At     time.Time `json:"at"`
	Kind   kind      `json:"kind"`
	Author string    `json:"author"`
	Text   string    `json:"text"`
}

func notice(author, what string) message {
	return message{Kind: kindNotice, Author: author, Text: what, At: time.Now()}
}

const listenerBuffer = 32

// room holds the channel of every connected socket and nothing else.
type room struct {
	readers map[chan message]struct{}
	mu      sync.Mutex
}

func newRoom() *room {
	return &room{readers: make(map[chan message]struct{})}
}

func (r *room) join() (<-chan message, func()) {
	ch := make(chan message, listenerBuffer)

	r.mu.Lock()
	r.readers[ch] = struct{}{}
	r.mu.Unlock()

	return ch, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.readers, ch)
	}
}

// broadcast delivers m to every socket whose buffer has room. A slow socket
// misses messages rather than holding up the room.
func (r *room) broadcast(m message) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for ch := range r.readers {
		select {
		case ch <- m:
		default:
		}
	}
}

const (
	maxNameRunes = 24
	maxTextRunes = 500
)

func cleanName(s string) string { return clean(s, maxNameRunes) }

func cleanText(s string) string { return clean(s, maxTextRunes) }

// clean drops control characters, folds runs of space and cuts s to limit
// runes.
func clean(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if runes := []rune(s); len(runes) > limit {
		s = strings.TrimSpace(string(runes[:limit]))
	}
	return s
}
