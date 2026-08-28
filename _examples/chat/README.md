# chat

A chat room in about 300 lines: `html/template` for the HTML, htmx for the
requests, and server-sent events for everything that arrives without one.

```bash
go run .
```

Then open <http://localhost:8080> in two windows and type a different name in
each.

Nothing is stored. Delivery is best-effort to windows connected at that moment,
and a slow window can miss messages when its small buffer is full. A window
that opens late starts empty. The room holds the channels of the connected
readers and nothing else.

The page loads version-pinned htmx and SSE extension assets from jsDelivr with
subresource-integrity and anonymous CORS checks, so the first load needs a
network. Every state-changing form carries a CSRF token backed by an HttpOnly,
SameSite cookie.

## What happens

| Step | Request | Answer |
| --- | --- | --- |
| Type a name | `POST /join`, from htmx | `HX-Redirect: /chat`, or the form again with the reason |
| Open the room | `GET /chat` | the whole page |
| Watch the room | `GET /events`, from an `EventSource` | a stream of rendered HTML |
| Send a message | `POST /messages`, from htmx | `204`, and `HX-Trigger: message-sent` |
| Leave the room | `POST /leave`, from htmx | `HX-Redirect: /` |

The answer to a message carries no HTML. The room attempts to deliver the
message to every connected window over the stream, the sender's window
included. A full listener buffer is skipped so one slow window cannot delay
the room. The page uses the same rendering path for its own messages and those
from somebody else.

## The three htmx pieces

**A redirect that htmx can follow.** htmx follows a `303` inside the request
that it made and swaps whatever the new page answers into the form. `HX()`
asks the browser to go there instead, and falls back to the `303` for a client
that runs no JavaScript:

```go
return c.HX().Redirect("/chat")
```

`middleware.HTMXRedirect` does the same to every redirect of a scope, for an
application with more pages than this one.

**An answer that swaps nothing.** `NoSwap` writes a `204`, which tells htmx to
leave the page alone. The headers of the chain still apply, so the same answer
fires the event that empties the input:

```go
return c.HX().Trigger("message-sent").NoSwap()
```

```html
<form action="/messages" method="post" hx-post="/messages" hx-on:message-sent="this.reset()">
	<input type="hidden" name="_csrf" value="{{.CSRFToken}}">
```

**A stream of HTML, not of JSON.** `SendComponent` renders a template into the
event, and the sse extension swaps it:

```go
return router.ServeSSE(c, ch, sendTo(c.User), router.SSEHeartbeat(20*time.Second))
```

```html
<div id="log" sse-swap="message,notice" hx-swap="beforeend"></div>
```

The sender is built per connection, so each window renders the same message for
itself and marks the ones that its own author wrote:

```go
func sendTo(reader string) router.SSESender[message] {
	return func(s *router.SSEWriter, m message) error {
		return s.SendComponent(string(m.Kind), tmpl(string(m.Kind), view{
			message: m,
			Own:     m.Author == reader,
		}))
	}
}
```

## The files

| File | Holds |
| --- | --- |
| `main.go` | the context type, the routes and the handlers |
| `room.go` | the broadcast, and the sender of one connection |
| `view.go` | the templates, the name in the cookie, and the input limits |
| `templates/` | the two pages and the three fragments |

## What a real room would add

A session that is signed instead of a name in a cookie, a history to replay
through `Last-Event-ID`, one room per URL, and a message rate limit. The room
counts one reader per window, so two tabs of the same name join twice. Its
fixed-size per-window buffers intentionally favor room-wide responsiveness over
guaranteed delivery.
