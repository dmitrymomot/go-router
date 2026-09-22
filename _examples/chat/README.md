# chat

A chat room in about 400 lines: `html/template` for the HTML, htmx for the requests, and server-sent events for everything that arrives without one.

```bash
go run .
```

Then open <http://localhost:8080> in two windows and type a different name in each.

Nothing is stored. Delivery is best-effort to windows connected at that moment, and a slow window can miss messages when its small buffer is full. A window that opens late starts empty. The room holds the channels of the connected readers and nothing else.

The page loads htmx 4.0.0 and its hx-sse extension from jsDelivr, pinned by version and checked with subresource integrity and anonymous CORS, so the first load needs a network. Every state-changing form carries a CSRF token backed by an HttpOnly, SameSite cookie.

To move to a newer htmx, compute each hash from the CDN and check it against the npm tarball, then paste it into `templates/layout.html` and `main_test.go`:

```bash
V=4.0.0
for F in dist/htmx.min.js dist/ext/hx-sse.min.js; do
	curl -fsSL "https://cdn.jsdelivr.net/npm/htmx.org@$V/$F" | openssl dgst -sha384 -binary | openssl base64 -A; echo "  $F (CDN)"
	curl -fsSL "https://registry.npmjs.org/htmx.org/-/htmx.org-$V.tgz" | tar -xzO "package/$F" | openssl dgst -sha384 -binary | openssl base64 -A; echo "  $F (npm)"
done
```

## What happens

| Step | Request | Answer |
| --- | --- | --- |
| Type a name | `POST /join`, from htmx | `HX-Redirect: /room`, or the form again with the reason; a browser without JavaScript gets the whole page |
| Open the room | `GET /room` | the whole page |
| Watch the room | `GET /room/events`, from hx-sse | a stream of rendered HTML |
| Send a message | `POST /room/messages`, from htmx | `204`, and `HX-Trigger: message-sent` |
| Leave the room | `POST /leave`, from htmx | `HX-Redirect: /` |

The answer to a message carries no HTML. The room attempts to deliver the message to every connected window over the stream, the sender's window included. A full listener buffer is skipped so one slow window cannot delay the room. The page uses the same rendering path for its own messages and those from somebody else.

## The two halves

**A door anybody may knock on**, and **a room only a named reader may enter**. The room is a router of its own, mounted once, so its name check sits at its door and no handler inside repeats it:

```go
r.Mount("/room", roomRouter())
```

The prefix appears at that line and nowhere else. `roomRouter` registers `/`, `/messages` and `/events`, and `Use(requireUser)` covers all three.

**A server that stops when its context does.** `serve.Run` owns the listener, the signal and the drain, so `main` says what the timeouts are and nothing about how to shut down. The room closes when the drain begins, because a drain that waits for an open stream never ends:

```go
OnDrain: rm.close,
```

## The htmx pieces

**A redirect that htmx can follow.** htmx follows a `303` inside the request that it made and swaps whatever the new page answers into the form. `HX()` asks the browser to go there instead, and falls back to the `303` for a client that runs no JavaScript:

```go
return c.HX().Redirect("/room")
```

`middleware.HTMXRedirect` does the same to every redirect of a scope, for an application with more pages than this one.

**One answer for htmx and for a plain form.** A refused name goes back as the form alone to htmx, which swaps it in place of the old one, and as the whole page to a browser that posted without JavaScript. `RenderPartial` picks by `HX-Request-Type` and adds it to `Vary`:

```go
return c.RenderPartial(http.StatusOK, tmpl("join", form), tmpl("index", form))
```

**An error that swaps nothing.** htmx 4 swaps a `4xx` or `5xx` answer like any other, so a plain-text `403` from the CSRF check would replace the form. Every form says to swap nothing for those:

```html
<form id="join" hx-post="/join" hx-swap="outerHTML" hx-status:4xx="swap:none" hx-status:5xx="swap:none">
```

**An answer that swaps nothing.** `NoSwap` writes a `204`, which tells htmx to leave the page alone. The headers of the chain still apply, so the same answer fires the event that empties the input:

```go
return c.HX().Trigger("message-sent").NoSwap()
```

```html
<form action="/room/messages" method="post" hx-post="/room/messages" hx-on:message-sent="this.reset()">
	<input type="hidden" name="_csrf" value="{{.CSRFToken}}">
```

**A stream of HTML, not of JSON.** `SendComponent` renders a template into the event, and the hx-sse extension swaps it into the element that connects. It swaps only an unnamed event, and a named one becomes a DOM event, so the room names none:

```go
return router.ServeSSE(c, ch, sendTo(c.User), router.SSEHeartbeat(20*time.Second))
```

```html
<div id="log" hx-sse:connect="/room/events" hx-swap="beforeend"
	hx-on::sse:after:message="this.scrollTop = this.scrollHeight"></div>
```

The sender is built per connection, so each window renders the same message for itself and marks the ones that its own author wrote:

```go
func sendTo(reader string) router.SSESender[message] {
	return func(s *router.SSEWriter, m message) error {
		return s.SendComponent("", tmpl(string(m.Kind), view{
			message: m,
			Own:     m.Author == reader,
		}))
	}
}
```

## The files

| File | Holds |
| --- | --- |
| `main.go` | the context type, the server, and the root router |
| `join.go` | the door: the form, the name check, and the two redirects |
| `chat.go` | the mounted room router and its three handlers |
| `room.go` | the broadcast, and the sender of one connection |
| `view.go` | the templates, the name in the cookie, and the input limits |
| `templates/` | the two pages and the three fragments |

## What a real room would add

A session that is signed instead of a name in a cookie, a history to replay through `Last-Event-ID`, one room per URL, and a message rate limit. The room counts one reader per window, so two tabs of the same name join twice. Its fixed-size per-window buffers intentionally favor room-wide responsiveness over guaranteed delivery.
