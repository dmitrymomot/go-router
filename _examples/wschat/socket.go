package main

import (
	"context"
	"encoding/json/v2"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/ws"
)

// incoming is what the page sends.
type incoming struct {
	Text string `json:"text"`
}

// socket is the whole chat protocol: a name in the query, JSON both ways.
func socket(c Ctx) error {
	name := cleanName(c.Query("name"))
	if name == "" {
		return router.ErrBadRequest.WithMessage("Pick a name of 1 to %d characters.", maxNameRunes)
	}

	// The default origin check lets in pages of this host alone, so a page
	// of another site cannot post into the room in the name of a visitor.
	conn, err := ws.Accept(c,
		ws.Subprotocols("chat.v1"),
		ws.ReadLimit(4<<10),
		ws.Heartbeat(20*time.Second),
		ws.WriteTimeout(10*time.Second),
	)
	if err != nil {
		return err // a refused handshake, which the error handler answers
	}
	defer conn.Close(ws.StatusNormalClosure, "") //nolint:errcheck // The client is gone if it fails.

	rm := c.Room
	ch, leave := rm.join()
	defer leave()
	rm.broadcast(notice(name, "joined the chat"))
	defer rm.broadcast(notice(name, "left the chat"))

	// One goroutine reads, so pings get their pongs while this one writes.
	// It must not touch c, which the router reuses once the handler returns,
	// so it gets the room and the connection alone. The router closes the
	// connection when the handler returns, which ends the Read.
	go readMessages(conn, rm, name)

	for {
		select {
		case <-conn.Done():
			// The client left, broke a rule, missed a heartbeat, or the
			// drain of serve.Run closed the socket with 1012.
			return nil
		case m := <-ch:
			if err := conn.WriteJSON(c, m); err != nil {
				return nil
			}
		}
	}
}

func readMessages(conn *ws.Conn, rm *room, name string) {
	for {
		typ, data, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		var in incoming
		if typ != ws.Text || json.Unmarshal(data, &in) != nil {
			_ = conn.Close(ws.StatusUnsupportedData, "send JSON text")
			return
		}
		if text := cleanText(in.Text); text != "" {
			rm.broadcast(message{Kind: kindMessage, Author: name, Text: text, At: time.Now()})
		}
	}
}
