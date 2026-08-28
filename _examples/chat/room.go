package main

import (
	"sync"
	"time"

	"github.com/dmitrymomot/go-router"
)

type kind string

const (
	kindMessage kind = "message"
	kindNotice  kind = "notice"
)

type message struct {
	At     time.Time
	Kind   kind
	Author string
	Text   string
}

func notice(author, what string) message {
	return message{Kind: kindNotice, Author: author, Text: what, At: time.Now()}
}

const listenerBuffer = 32

type room struct {
	readers  map[chan message]struct{}
	mu       sync.Mutex
	shutdown bool
}

func newRoom() *room {
	return &room{readers: make(map[chan message]struct{})}
}

func (r *room) join() (<-chan message, func()) {
	ch := make(chan message, listenerBuffer)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.shutdown {
		close(ch)
		return ch, func() {}
	}
	r.readers[ch] = struct{}{}

	return ch, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if _, ok := r.readers[ch]; ok {
			delete(r.readers, ch)
			close(ch)
		}
	}
}

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

func (r *room) close() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.shutdown = true
	for ch := range r.readers {
		delete(r.readers, ch)
		close(ch)
	}
}

type view struct {
	message
	Own bool
}

func sendTo(reader string) router.SSESender[message] {
	return func(s *router.SSEWriter, m message) error {
		return s.SendComponent(string(m.Kind), tmpl(string(m.Kind), view{
			message: m,
			Own:     m.Author == reader,
		}))
	}
}
