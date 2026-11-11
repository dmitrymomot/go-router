package ws_test

import (
	"net/http"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/ws"
)

type Context struct{ router.Base }

func newContext(http.ResponseWriter, *http.Request) *Context { return new(Context) }

// An echo endpoint that speaks the "chat.v1" subprotocol, lets pages of
// app.example.com connect, and pings every 30 seconds.
func ExampleAccept() {
	r := router.New(newContext)

	r.GET("/ws", func(c *Context) error {
		conn, err := ws.Accept(c,
			ws.Subprotocols("chat.v1"),
			ws.Origins("https://app.example.com"),
			ws.Heartbeat(30*time.Second),
		)
		if err != nil {
			return err // the error handler answers the refused handshake
		}
		defer conn.Close(ws.StatusNormalClosure, "") //nolint:errcheck // The client is gone if it fails.

		for {
			typ, msg, err := conn.Read(c)
			if err != nil {
				// The client left, broke a rule, or the server drains.
				return nil
			}
			if err := conn.Write(c, typ, msg); err != nil {
				return nil
			}
		}
	})
	_ = r
}

type Note struct {
	Text string `json:"text"`
}

// A handler that pushes JSON from a channel, and ends when the client closes
// the connection.
func ExampleConn_WriteJSON() {
	r := router.New(newContext)
	notes := make(chan Note)

	r.GET("/notes", func(c *Context) error {
		conn, err := ws.Accept(c)
		if err != nil {
			return err
		}
		defer conn.Close(ws.StatusNormalClosure, "") //nolint:errcheck // The client is gone if it fails.

		// Read in the background, so pings get their pongs and the close of
		// the client ends the loop below.
		go func() {
			for {
				if _, _, err := conn.Read(c); err != nil {
					return
				}
			}
		}()
		for {
			select {
			case <-conn.Done():
				return nil
			case n := <-notes:
				if err := conn.WriteJSON(c, n); err != nil {
					return nil
				}
			}
		}
	})
	_ = r
}
