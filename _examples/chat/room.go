package main

import (
	"sync"
	"time"

	"github.com/dmitrymomot/go-router"
)

// kind tells a message from a notice. The stream carries it as the name of the
// event, and the page lists both names in its sse-swap attribute.
type kind string

const (
	kindMessage kind = "message"
	kindNotice  kind = "notice"
)

// message is one thing that the room shows. It never reaches a disk: the room
// hands it to whoever is connected and then forgets it.
type message struct {
	At     time.Time
	Kind   kind
	Author string
	Text   string
}

// notice returns the message that tells the room what a reader did.
func notice(author, what string) message {
	return message{Kind: kindNotice, Author: author, Text: what, At: time.Now()}
}

// listenerBuffer is the number of messages that one reader may fall behind by.
// The room drops what does not fit rather than wait, so one slow browser never
// holds up the rest.
const listenerBuffer = 32

// room broadcasts messages to the readers that are connected.
//
// It is the whole storage of this application, and it holds only the channels
// of the readers that are connected right now.
type room struct {
	readers  map[chan message]struct{}
	mu       sync.Mutex
	shutdown bool
}

func newRoom() *room {
	return &room{readers: make(map[chan message]struct{})}
}

// join adds a reader and returns its channel and the function that removes it
// again. Call the second one when the request ends.
//
// The channel closes when the reader leaves or the room shuts down, which is
// what ends [router.ServeSSE].
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

// broadcast hands m to every reader. A reader whose buffer is full loses the
// message, because a room that waits for one browser stops for all of them.
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

// close ends every stream, which lets the server shut down.
func (r *room) close() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.shutdown = true
	for ch := range r.readers {
		delete(r.readers, ch)
		close(ch)
	}
}

// view is one message as one reader sees it.
type view struct {
	message

	// Own reports that this reader wrote the message, which puts it on the
	// other side of the log.
	Own bool
}

// sendTo returns the sender of one connection. The router calls it for every
// message that the channel of that connection carries, and it writes the HTML
// that the sse extension of htmx swaps into the page.
//
// The reader is a parameter, so each connection renders the same message for
// itself and a page marks the messages of its own author.
func sendTo(reader string) router.SSESender[message] {
	return func(s *router.SSEWriter, m message) error {
		return s.SendComponent(string(m.Kind), tmpl(string(m.Kind), view{
			message: m,
			Own:     m.Author == reader,
		}))
	}
}
