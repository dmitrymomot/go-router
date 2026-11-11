# wschat

A chat room over WebSocket, built on package `ws`: one static page, about a hundred lines of plain JavaScript, and a server with no dependency beyond go-router.

```bash
go run .
```

Then open <http://localhost:8080> in two windows and type a different name in each.

Nothing is stored. Delivery is best-effort to the sockets connected at that moment, and a slow socket misses messages when its small buffer is full. [`_examples/chat`](../chat) is the same room with htmx and server-sent events, for a comparison of the two.

## What happens

| Step | Request | Answer |
| --- | --- | --- |
| Open the page | `GET /`, `/app.js`, `/style.css` | the embedded files, under a CSP that allows no inline script |
| Type a name | `GET /ws?name=Ann`, an upgrade with the subprotocol `chat.v1` | `101`, or `400` for a blank name and `403` for a page of another origin |
| Send a message | a text frame `{"text":"hi"}` | the message as JSON, to every socket in the room, the sender's included |
| Send anything else | a binary frame, or text that is not JSON | a close frame with `1003` |
| Leave | a close frame with `1000` | the close handshake, and a notice to the room |

## The server

`socket` in `socket.go` is the whole protocol. It checks the name, then `ws.Accept` answers the handshake:

```go
conn, err := ws.Accept(c,
	ws.Subprotocols("chat.v1"),
	ws.ReadLimit(4<<10),
	ws.Heartbeat(20*time.Second),
	ws.WriteTimeout(10*time.Second),
)
```

The default origin check lets in pages of this host alone, so a page of another site cannot open a socket with the visitor's cookies. `ws.Origins` lists more origins when the page lives elsewhere.

One goroutine reads and broadcasts, the handler writes what the room sends, and `conn.Done()` ends the handler once the socket closes for any reason. The reading goroutine gets the room and the connection, never the context, which the router reuses once the handler returns:

```go
go readMessages(conn, rm, name)

for {
	select {
	case <-conn.Done():
		return nil
	case m := <-ch:
		if err := conn.WriteJSON(c, m); err != nil {
			return nil
		}
	}
}
```

**A server that drains its sockets.** `serve.Run` closes the drain channel when it stops, and every open socket gets a close frame with `1012` (service restart). The page reconnects at once on that code and backs off on any other, so a deploy costs the room a blink.

## The page

`static/app.js` opens the socket, renders each message with `textContent`, so no text of a user ever becomes HTML, and reconnects when the socket closes. The page loads nothing from another origin, so the CSP is `default-src 'self'`; `connect-src 'self'` covers `ws://` and `wss://` to the same host.

## The files

| File | Holds |
| --- | --- |
| `main.go` | the context type, the server, the router and the static files |
| `socket.go` | the WebSocket handler and the reader of one socket |
| `room.go` | the broadcast, the message type and the input limits |
| `static/` | the page, its script and its style |

## What a real room would add

A signed session instead of a name in the query, a history to send on connect, one room per URL, and a rate limit on messages.
