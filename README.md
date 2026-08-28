# go-router

An HTTP router for Go that hands your own struct to every handler.

```go
func listPosts(c *app.Context) error {
	posts, err := c.DB.Posts(c, c.User.ID)   // c is *app.Context, not an interface
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, posts)
}
```

The handler signature is `func(C) error`, where `C` is a type that you declare.
No type assertion, no `c.Get("user").(*User)`, no global state. Routing follows
[chi](https://github.com/go-chi/chi): `Route`, `Group`, `Use` and a `Mount` that
keeps the parameters of the prefix readable inside the mounted routes.

Requires **Go 1.27**. No dependencies.

```
go get github.com/dmitrymomot/go-router
```

## Quick start

```go
package main

import (
	"database/sql"
	"log"
	"net/http"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// Context is your request context. Embed router.Base, add what you need.
type Context struct {
	router.Base

	DB   *sql.DB
	User *User
}

// Ctx spares you from writing *Context at every middleware call.
type Ctx = *Context

func main() {
	db := openDB()

	// The factory supplies your fields. The router fills the embedded Base
	// with the request, the response and the route parameters.
	r := router.New(func(w http.ResponseWriter, req *http.Request) *Context {
		return &Context{DB: db}
	})

	r.Use(
		middleware.Recover[Ctx],
		middleware.RequestID[Ctx],
		middleware.RealIP[Ctx],
		middleware.Logger[Ctx],
	)

	r.GET("/health", func(c *Context) error {
		return c.String(http.StatusOK, "ok")
	})

	r.Route("/v1", func(v1 *router.Router[*Context]) {
		v1.Use(authenticate)

		v1.GET("/users/{id}", getUser)
		v1.POST("/users", createUser)
	})

	log.Fatal(http.ListenAndServe(":8080", r))
}

func getUser(c *Context) error {
	id, err := c.ParamAs[int]("id")
	if err != nil {
		return err
	}
	u, err := findUser(c, c.DB, id)
	if err != nil {
		return router.ErrNotFound.WithMessage("no user %d", id).WithError(err)
	}
	return c.JSON(http.StatusOK, u)
}

func createUser(c *Context) error {
	in, err := c.Bind[CreateUser]()
	if err != nil {
		return err
	}
	u, err := insertUser(c, c.DB, in)
	if err != nil {
		return err   // becomes a 500; the message never reaches the client
	}
	return c.JSON(http.StatusCreated, u)
}
```

`http.ListenAndServe` keeps the example short. Reach for
[the `serve` package](#running-the-server) for a server that stops on a signal and
drains what is in flight.

## The context

`router.Context` is an interface with one unexported method, so the only way to
satisfy it is to embed `router.Base`. That is what lets the router write route
parameters onto a type it has never seen.

```go
type Context struct {
	router.Base       // by value: one allocation per request
	DB   *sql.DB
	User *User
}
```

`Base` gives you the request state and the render helpers:

| | |
|---|---|
| `Request()` `SetRequest(r)` `Response()` `ResponseWriter()` | the request and the response wrapper |
| `Method()` `Path()` `URL()` `Header()` `SetHeader(k, v)` | the request line and its headers |
| `Param(name)` `ParamOK(name)` `ParamAs[T](name)` `ParamNames()` `RoutePattern()` | route parameters |
| `Host()` `RouteHost()` `Scheme()` `IsTLS()` `IsWebSocket()` | the host, the scheme and the kind of connection |
| `UserAgent()` `Referer()` `Accepts(offers...)` | what the client says about itself |
| `Query(name)` `QueryOK(name)` `QueryAs[T](name)` `QueryAsDefault(name, def)` `QueryAllAs[T](name)` `QueryValues()` | query parameters |
| `FormValue(name)` `FormDefault(name, def)` `FormAs[T](name)` `FormValues()` | fields of the posted body |
| `FormFile(name)` `FormFiles(name)` `MultipartForm()` | uploaded files |
| `Bind[T]()` `BindJSON[T]()` `BindForm[T]()` `BindQuery[T]()` `BindPath[T]()` `BindHeader[T]()` | [request decoding](#binding) |
| `Cookie(name)` `SetCookie(c)` `SignedCookie` `SetSignedCookie` `AddFlash` `Flashes` | [cookies and flash messages](#signed-cookies-and-flash-messages) |
| `JSON` `JSONPretty` `String` `Stringf` `HTML` `Blob` `Stream` `NoContent` `Redirect` | response rendering |
| `File` `FileFS` `AttachmentFile` `InlineFile` `Attachment` `Inline` | [files and downloads](#files-and-downloads) |
| `Render` `RenderStream` `RenderPartial` | template components, such as [templ](https://templ.guide) |
| `HXRequest()` `HXTarget()` `HXRedirect(url)` and the rest | [htmx](#htmx) |
| `SSE(status, opts...)` `LastEventID()` | [server-sent events](#server-sent-events) |
| `Set(k, v)` `Get(k)` | per-request values |
| `Logger()` | the logger of the router, or `slog.Default` |
| `Deadline` `Done` `Err` `Value` | `Base` is a `context.Context` |

`Scheme` reads the TLS state of the connection first, then `X-Forwarded-Proto`.
It takes that header only when it names `http` or `https`, because the value
goes back to the client in the `Location` of an absolute redirect. Trust it as
far as you trust the proxy in front of the server, and not at all where clients
connect directly.

`Accepts` returns the offer the client prefers, or an empty string when it takes
none. Name the offers in the order the server prefers, because that order
settles a tie:

```go
switch c.Accepts(router.MIMETextHTML, router.MIMEApplicationJSON) {
case router.MIMETextHTML:
	return c.Render(http.StatusOK, view.Order(order))
default:
	return c.JSON(http.StatusOK, order)
}
```

Because `Base` implements `context.Context`, you pass the context itself
straight to anything that takes one:

```go
rows, err := c.DB.QueryContext(c, "...")
```

Add your own methods; they are ordinary methods on your own type:

```go
func (c *Context) Admin() bool { return c.User != nil && c.User.Role == "admin" }
```

## Routing

```go
r.GET(pattern, handler, middleware...)     // and HEAD POST PUT PATCH DELETE OPTIONS
r.Handle(method, pattern, handler, ...)
r.Any(pattern, handler, ...)               // any method no explicit route answers
r.Match([]string{"GET", "POST"}, pattern, handler, ...)
```

`Any` registers **one** entry that answers whatever arrives, rather than one
route per standard method, so it also answers a method no list holds, such as
`QUERY` or a WebDAV method. An explicit method always wins, whichever
registration came first:

```go
r.Any("/webhooks/{id}", forward)
r.POST("/webhooks/{id}", record)   // POST reaches record, everything else forward
```

Such a route answers every method, so it never produces a 405 and never adds to
an `Allow` header. `r.Routes()` reports it once, under the method `*`.

`router.MethodQuery` is the `QUERY` method of RFC 10008, which `net/http` has no
constant for yet.

Routes can also answer one host; see [Host routing](#host-routing).

### Pattern syntax

| Pattern | Matches |
|---|---|
| `/users` | that path exactly |
| `/users/{id}` | one segment, readable as `c.Param("id")` |
| `/orders/{id:[0-9]+}` | one segment that the regular expression accepts |
| `/reports/rep-{date}.csv` | part of a segment; `c.Param("date")` is `20260102`, not the file name |
| `/files/{name}.{ext}` | two parameters in one segment |
| `/files/{path...}` | the rest of the path, including slashes |
| `/assets/*` | the rest of the path, readable as `c.Param("*")` |

Children are tried in the order static, regular expression, parameter,
catch-all, so a literal path always wins. The walk backtracks: with
`/a/{x}/c` and `/a/b/d` registered, `/a/b/c` still reaches the first route.

In a segment that mixes text with parameters, literals bind as far right as
the segment allows, so each parameter takes as much as it can. That is one rule
for both readings you would expect: `rep-{date}.csv` reads `a.csv` out of
`rep-a.csv.csv`, and `{name}.{ext}` splits `a.b.txt` into `a.b` and `txt`. No
parameter may be empty, so `rep-.csv` does not match. Two parameters side by
side, as in `{a}{b}`, are a registration error: put text between them.

A catch-all still has to span a whole segment.

`/users/` and `/users` are the same path. Call `r.RedirectTrailingSlash(true)`
to answer 301 (308 for a method with a body) instead.

A malformed or conflicting pattern **panics at startup**, not at request time.

### Scopes

```go
r.Use(mw...)                       // applies to this scope and everything below
r.Group(func(g *R) { ... })        // same prefix, its own middleware
r.Route("/admin", func(g *R) {})   // a prefix and its own middleware
r.With(rateLimit).POST("/login", login)
```

`Use` panics once the scope has routes, because middleware added later would
silently skip the routes above it. Open a `Group` for that.

A scope that carries a prefix owns its own fallbacks, so an API branch answers a
miss as JSON while the pages around it answer with a page:

```go
r.Route("/api", func(g *router.Router[*Context]) {
	g.NotFound(func(c *Context) error { return c.JSON(404, apiError{}) })
	g.GET("/users/{id}", getUser)
})
```

The middleware of the scope runs around that 404 and 405, the way the middleware
of a host does. The innermost scope covering the path wins, and a prefix segment
that holds a parameter still covers it: the scope of `/t/{tenant}` owns the 404
of `/t/acme/typo`.

`Pre` is the stage before matching. Middleware there sees the request while the
path still decides the route, which is what a rewrite and a method override
need:

```go
r.Pre(middleware.MethodOverride[Ctx])
```

Only the root accepts `Pre`, because a scope cannot own a stage that runs before
matching picks the scope. Inside it `RoutePattern()` and `Param()` are still
empty, and an error goes to the error handler of the root.

### Mount

Three kinds, for three situations:

```go
// 1. Same context type. The routes join the parent trie, so a parameter in
//    the prefix stays readable inside them and matching costs no extra work.
r.Route("/t/{tenant}", func(g *router.Router[*Context]) {
	g.Mount("/api", apiRouter)
})
// GET /t/{tenant}/api/users/{id}   -> c.Param("tenant") and c.Param("id")

// 2. A different context type. The mounted router serves the request itself,
//    with its own factory, middleware and error handler, and sees the path
//    with the prefix removed.
r.MountRouter("/admin", adminRouter)   // *admin.Context

// 3. Any http.Handler, the way http.StripPrefix works.
r.MountHandler("/static", http.FileServerFS(assets))
```

For the files of a front-end build, reach for [the `static` package](#static-assets)
instead of a bare `http.FileServerFS`: it adds the build tag, the cache headers
and the single page fallback.

`r.Routes()` returns every registered route, with the host pattern that owns
it, which is handy in a startup log or a test that guards the route table.

### Mounting a subsystem with its own context

Use `MountRouter` when one part of the service needs different dependencies on
its context. The admin area below carries an `*admin.Context` with an audit
log and a signed-in operator, while the public API keeps its own `*app.Context`.
Neither type knows about the other.

```go
// admin/admin.go
package admin

type Context struct {
	router.Base

	Audit    *audit.Log
	Operator *Operator      // never nil inside this router
}

func (c *Context) Log(action string) { c.Audit.Record(c.Operator.ID, action) }

// Router builds the admin subsystem. It owns its factory, its middleware, its
// error handler and its fallbacks.
func Router(audit *audit.Log, db *sql.DB) *router.Router[*Context] {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context {
		return &Context{Audit: audit}
	})

	r.Use(middleware.Recover[*Context])
	r.Use(requireOperator(db))        // fills Context.Operator or returns 401

	// Admin answers HTML, so it renders its errors as a page.
	r.ErrorHandler(func(c *Context, err error) {
		_ = c.HTML(router.StatusOf(err), errorPage(err))
	})
	r.NotFound(func(c *Context) error { return c.HTML(404, notFoundPage()) })

	r.GET("/users/{id}", showUser)
	r.POST("/users/{id}/suspend", suspendUser)
	return r
}

func suspendUser(c *Context) error {
	id, err := c.ParamAs[int]("id")
	if err != nil {
		return err
	}
	c.Log("suspend user")              // a method on the admin context
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/admin/users/%d", id))
}
```

```go
// main.go
api := router.New(func(http.ResponseWriter, *http.Request) *app.Context {
	return &app.Context{DB: db}
})
api.Use(
	middleware.Recover[*app.Context],
	middleware.RequestID[*app.Context],
)
api.GET("/v1/users/{id}", getUser)          // *app.Context

api.MountRouter("/admin", admin.Router(auditLog, db))

http.ListenAndServe(":8080", api)
```

A request to `POST /admin/users/7/suspend` reaches `suspendUser` with an
`*admin.Context`. The admin router sees the path as `/users/7/suspend`, so its
patterns never repeat the prefix, and `c.Param("id")` is `7`.

The two routers stay independent at the seam:

| | outer router | mounted router |
|---|---|---|
| context type | `*app.Context` | `*admin.Context` |
| context factory | its own | its own |
| middleware | runs first, on the outer context | runs after, on its own context |
| error handler | JSON | the HTML page above |
| `NotFound` | the outer one | the admin one, for any path under `/admin` |
| path a handler sees | `/admin/users/7/suspend` | `/users/7/suspend` |

Two consequences worth knowing before you reach for it:

- A parameter of the mount prefix does **not** cross the seam. The mounted
  router re-matches the stripped path and knows nothing about the outer match,
  so `r.MountRouter("/t/{tenant}", sub)` leaves `c.Param("tenant")` empty
  inside `sub`. Read it in an outer middleware and pass it on through a header
  or the request context, or use `Mount` and one context type instead.
- Matching runs twice, once per router. That is a second trie walk per request,
  which `Mount` avoids by joining the parent trie.

Reach for `MountRouter` when the subsystem genuinely wants its own context, and
for `Mount` when it is the same application split across files.

### Named routes and URL building

`Name` opens a scope whose **next** route carries the name. `URL` then builds a
path from it, so a link survives a change of pattern:

```go
r.Name("post").GET("/blog/{year}/{slug}", showPost)

r.URL("post", map[string]string{"year": "2026", "slug": "hello"})  // "/blog/2026/hello", error
r.MustURL("post", "year", "2026", "slug", "hello")                 // "/blog/2026/hello", panics
```

`URL` takes named parameters, not positional ones, so the call reads the same
whichever order the pattern declares them in. It reports an error for a name no
route carries, for a parameter the pattern declares and the map leaves out, and
for one the map holds and the pattern does not. Each value is percent-encoded; a
`{rest...}` value keeps its separators and every segment between them is
encoded.

`MustURL` panics instead, which is what you want where a link is built from
constants. Reach for `URL` where a name or a value comes from data.

A second route on one `Name` scope, and a name another route already carries,
are both registration errors. The result is a path and not an absolute URL: a
named route inside a host scope resolves to its path alone, because the host
pattern carries parameters the route knows nothing about.

`Meta` attaches an arbitrary value to the next routes, which `r.Routes()` reports
back. The router never reads it, so it carries whatever a generator outside this
module puts on a route:

```go
r.Name("user").Meta(openapi.Op{Summary: "read a user"}).GET("/users/{id}", getUser)
```

`r.Routes()` returns every route with its method, pattern, host, name and meta.
`r.Build()` compiles the trie now and returns a malformed or conflicting pattern
as an error instead of panicking, which suits a table that comes from
configuration and a test that asserts a conflict without `recover`.

`r.Observe(fn)` calls `fn` once per request, after it is answered, with the
status the client saw, the body size, the duration and the error that reached
the error handler. It runs for a request that matched no route, one whose method
no route answers, and one whose handler panicked, which is what a route-level
metric needs and what wrapping each handler cannot give:

```go
r.Observe(func(c router.Context, status int, size int64, d time.Duration, err error) {
	requests.WithLabelValues(c.RoutePattern(), strconv.Itoa(status)).Observe(d.Seconds())
})
```

The observer runs after the error handler wrote the response, so it must not
write to it. A router without one pays a single nil check per request.

## Host routing

`Host` opens a scope that answers one host. Everything inside it — routes,
middleware, `NotFound`, `MethodNotAllowed`, `ErrorHandler` — belongs to that
host alone.

```go
r := router.New(newCtx)
r.Use(middleware.Recover[Ctx](), middleware.Logger[Ctx]())   // every host

// 1. The main site.
r.Host("example.com", func(h *router.Router[Ctx]) {
	h.GET("/", landing)
	h.Route("/blog", func(b *router.Router[Ctx]) {
		b.GET("/", blogIndex)
		b.GET("/{slug}", blogPost)
	})
})

// 2. A fixed subdomain, with middleware and a 404 of its own.
r.Host("api.example.com", func(h *router.Router[Ctx]) {
	h.Use(apiKey)
	h.NotFound(func(c *Context) error { return c.JSON(404, errBody) })
	h.GET("/v1/users/{id}", getUser)
})

// 3 + 4. One tenant application, on a subdomain or on a domain the tenant
//        brought along. Both host patterns share one scope.
r.Hosts([]string{"{tenant}.example.com", "*"}, func(h *router.Router[Ctx]) {
	h.Use(resolveTenant)              // c.Param("tenant"), or c.Host()
	h.GET("/", dashboard)
})

// A route outside every host scope answers on any host.
r.GET("/healthz", health)
```

```go
func resolveTenant(next router.HandlerFunc[Ctx]) router.HandlerFunc[Ctx] {
	return func(c *Context) error {
		t, err := c.DB.Tenant(c, c.Param("tenant"), c.Host())   // slug, else domain
		if err != nil {
			return router.ErrNotFound.WithError(err)
		}
		c.Tenant = t
		return next(c)
	}
}
```

### Pattern syntax

| Host pattern | Matches |
|---|---|
| `example.com` | that host exactly |
| `{tenant}.example.com` | one label, readable as `c.Param("tenant")` |
| `{tenant:[a-z0-9-]+}.example.com` | one label that the regular expression accepts |
| `acme-{env}.example.com` | part of a label |
| `{sub...}.example.com` | one or more leading labels, as one value |
| `*.example.com` | one label, with no value kept |
| `*` | any host — this is how a custom domain arrives |

A host parameter is an ordinary route parameter: `c.Param("tenant")` reads it,
and it comes before the parameters of the path in `ParamNames()`. A name that
the host and the path both declare is a registration error.

The router matches the host without its port, without a trailing dot and in
lower case, so `example.com` also answers `Example.com:8080` in development. A
port inside a pattern is a registration error.

### Which host wins

A fixed host beats a pattern, a pattern with more static labels beats one with
fewer, and `*` comes last. So with all four cases above registered:

| Request host | Answers |
|---|---|
| `example.com` | `example.com` |
| `api.example.com` | `api.example.com` |
| `acme.example.com` | `{tenant}.example.com`, `c.Param("tenant")` is `acme` |
| `acme.com` | `*`, `c.Host()` is `acme.com` |

Registering `www.example.com` takes it out of `{tenant}.example.com`, which is
how you reserve a subdomain.

### The fallbacks

- A route that **no** host scope registered answers **every** host. It is also
  the fallback when the matched host has no route for the path, which is what
  puts `/healthz` on all four hosts above.
- A host that matches **no** pattern still reaches those host-free routes, and
  then the `NotFound` of the root.
- With no match anywhere the answer is **404**.
- `NotFound`, `MethodNotAllowed` and `ErrorHandler` inside a host scope apply
  to that host. Each falls back to the one of the root while it is unset.
  A route that answers every host uses the ones of the root.

### One error handler per host

```go
// JSON for the API.
r.Host("api.example.com", func(h *router.Router[Ctx]) {
	h.ErrorHandler(func(c *Context, err error) {
		_ = c.JSON(router.StatusOf(err), errorBody(err))
	})
	h.GET("/v1/users/{id}", getUser)
})

// A branded page for the main site.
r.Host("example.com", func(h *router.Router[Ctx]) {
	h.ErrorHandler(func(c *Context, err error) {
		_ = c.HTML(router.StatusOf(err), sitePage(err))
	})
	h.GET("/", landing)
})

// A page in the colours of the tenant. The host parameter is on the context
// before the error handler runs, on a 404 as well as on a returned error.
r.Hosts([]string{"{tenant}.example.com", "*"}, func(h *router.Router[Ctx]) {
	h.Use(resolveTenant)
	h.ErrorHandler(func(c *Context, err error) {
		_ = c.HTML(router.StatusOf(err), tenantPage(c.Param("tenant"), c.Host(), err))
	})
	h.GET("/", dashboard)
})
```

The handler renders every failure of that host: an error a handler or a
middleware returned, a panic that escaped the chain, and a 404 or a 405 while
`NotFound` and `MethodNotAllowed` are unset. `c.Param(...)` and `c.Host()` read
the same values they do inside a handler.

`c.Tenant`, on the other hand, is only set when `resolveTenant` reached that
far. A 404 for an unknown tenant runs the middleware first, so it is; a 404 on
a host that no scope claims never enters the scope at all, and the root handler
renders it.

### A host with its own context type

`HostRouter` hands a whole host to a router that carries a different context
type, with its own factory, middleware, error handler and fallbacks. It is the
host analogue of `MountRouter`, and the path reaches it unchanged.

```go
r.HostRouter("api.example.com", api.Router(db))       // *api.Context
r.HostHandler("docs.example.com", http.FileServerFS(docs))
```

The middleware of `r` still runs in front of it, which is where a recover, a
request id and a log line belong. A parameter of the host pattern does not
cross the seam; read it in a middleware of `r` and pass it on.

### Cost

The host lookup runs once, before the path. A fixed host is one map lookup; a
pattern is one suffix comparison and one byte scan. Each host owns its own
route trie, so the path match after it costs exactly what it costs in a router
without host routes, and the whole step allocates nothing. A router that
registers no host scope never enters the code at all.

## Errors

A handler returns `error`. The router hands it to the error handler.

```go
return router.ErrNotFound                                  // 404 "Not Found"
return router.ErrForbidden.WithMessage("not your order")   // 403, your text
return router.ErrUnprocessableEntity.WithDetails(fieldErrors)
return fmt.Errorf("query users: %w", err)                  // 500, message hidden
```

`errors.Is` matches on the status code, so `errors.Is(err, router.ErrNotFound)`
is true for any 404 that your code produced.

One error handler renders **every** failure of a request:

- an error a handler or a middleware returned,
- a panic that escaped the chain, as a 500 carrying the panic and its stack,
- a path with no route, as `ErrNotFound`,
- a method no route answers, as `ErrMethodNotAllowed`.

The last two reach it only while `NotFound` and `MethodNotAllowed` are unset.
**A handler set there wins** and answers the request itself.

The default handler writes JSON unless the client asked for a text type, logs
the internal cause with `log/slog` at the level the status names — error for a
5xx, warning for a 4xx, which is the client and not the server — and never puts
an internal message in the body. An htmx request is the exception: it gets the message as escaped HTML,
because htmx swaps what it receives into a page and a JSON error would land
there as text. Replace the handler wholesale when you need a different shape:

```go
r.ErrorHandler(func(c *Context, err error) { ... })
r.NotFound(func(c *Context) error { ... })
r.MethodNotAllowed(func(c *Context) error { ... })
```

`router.ErrorHandler[C](exposeCause)` is that default handler with a switch. With
`exposeCause` it writes the internal cause into the body as well, which is for
development and nothing else; with false it is `DefaultErrorHandler` down to the
bytes:

```go
if dev {
	r.ErrorHandler(router.ErrorHandler[*app.Context](true))
}
```

The router answers `405` with an `Allow` header on its own, answers `OPTIONS`
with `204` and an `Allow` header, and serves `HEAD` from the `GET` handler.

### Naming a status from outside the router

`StatusCoder` lets a package that does not import the router choose the status of
its own errors:

```go
func (e *NotFoundError) StatusCode() int { return http.StatusNotFound }
```

`StatusOf(err)` reads it after `HTTPError`. The message of such an error still
never reaches the client, because only an `HTTPError` carries text meant for it.

`ResolveStatus(res, err)` reports the status the client actually saw: the
committed status when the handler already wrote one, otherwise the status `err`
produces. Middleware needs both halves, because the error handler runs after the
chain unwinds and the response is still uncommitted while the middleware reads
it:

```go
err := next(c)
status := router.ResolveStatus(c.Response(), err)
```

### Problem documents

`ProblemError` carries the members of RFC 9457, and `ProblemErrorHandler` writes
them as `application/problem+json`:

```go
r.ErrorHandler(router.ProblemErrorHandler[*app.Context](dev))

return &router.ProblemError{
	Type:   "https://example.com/probs/insufficient-funds",
	Title:  "The account holds too little credit",
	Status: http.StatusConflict,
	Detail: "the balance is 30 and the transfer asks for 50",
}
```

The handler answers every error, not only a `ProblemError`. An `HTTPError` brings
its status, its message as the `detail` and its details as an `errors` member;
any other error carries the standard text of its status alone. `ProblemError`
also satisfies `StatusCoder`, so a router that keeps `DefaultErrorHandler`
answers the same status in that handler's shape.

### Panics

A panic never kills the connection: the router recovers it and hands it to the
error handler. `middleware.Recover` is still useful, because it catches the
panic *inside* the chain, where a logger above it still records the request.
`http.ErrAbortHandler` passes through both untouched.

The internal cause of that 500 is a `*PanicValue`, so an error tracker reads the
value and the stack as fields instead of cutting them out of a message:

```go
if pv, ok := errors.AsType[*router.PanicValue](err); ok {
	tracker.Report(pv.Value, pv.Stack)
}
```

The stack is capped at `DefaultStackSize`, 8 KiB, so a runaway recursion does not
write megabytes into one log record. It reaches the error handler and never the
client.

## Binding

`Bind` reads the media type and dispatches; a `GET`, `HEAD` or `DELETE` reads
the query string instead of a body.

```go
in, err := c.Bind[CreateUser]()        // JSON, form or query
in, err := c.BindQuery[Filter]()       // always the query string
id, err := c.ParamAs[uuid.UUID]("id")  // any type that parses from text
page := c.QueryAsDefault("page", 1)
```

Each source has a binder of its own, and each reads its own tag first:

| | reads | tag |
|---|---|---|
| `Bind[T]()` | the media type decides | as below |
| `BindJSON[T](opts...)` | the body as JSON | `json` |
| `BindForm[T]()` | the posted body | `form`, then `json` |
| `BindQuery[T]()` | the query string | `query`, then `json` |
| `BindPath[T]()` | the parameters of the matched route | `param`, then `json` |
| `BindHeader[T]()` | the request headers | `header`, then `json` |

A tag that names nothing falls back to the field name itself. `net/http` stores
a header under its canonical name, so a `header` tag matches in any case:
`x-request-id` finds `X-Request-Id`. A path parameter the route does not declare
leaves its field alone.

```go
type ref struct {
	Org  string `param:"org"`
	Repo string `param:"repo"`
}
```

JSON uses `encoding/json/v2`, which rejects invalid UTF-8 and duplicate object
names. Set options once per router:

```go
r.JSONOptions(json.RejectUnknownMembers(true))
```

The form and query decoder handles strings, booleans, numbers, `time.Duration`,
anything that implements `encoding.TextUnmarshaler` (including `time.Time`),
pointers, slices, and embedded structs. A `format` tag names the layout of a
time:

```go
type report struct {
	Day time.Time `query:"day" format:"2006-01-02"`
}
```

Without one, a `time.Time` reads RFC 3339.

### Field errors

A request that does not fit reports **every** field that failed, not only the
first, so a form re-renders with a message on each of them. The failures land in
`HTTPError.Details` as a `[]FieldError`, and the default handler writes them
under `details`:

```json
{"status":400,"error":"invalid request","details":[{"field":"age","message":"is not a number"}]}
```

`FieldError` is an ordinary error, so a type can report its own:

```go
func (in *CreateUser) Validate() error {
	if !strings.Contains(in.Email, "@") {
		return router.FieldError{Field: "email", Message: "is not an address"}
	}
	return nil
}
```

Every `Bind` method calls `Validate` after it fills the value, so the check lives
on the type it guards and nothing has to be registered. An error from it produces
a 422. Return one `FieldError`, or several joined with `errors.Join`, and the
client reads the failures field by field.

### Strict binding

By default a field no tag names still reads the key its own name spells, which is
convenient and means a request can reach a field the type never meant to expose.
`r.StrictBind(true)` fills only the fields a `form`, `query`, `json`, `param` or
`header` tag names. It is opt-in, so the default behaviour is unchanged.

### Values and forms

`ParseValue[T]` is the binder's own scalar table, exposed on its own, for a
string that came from somewhere else:

```go
d, err := router.ParseValue[time.Duration]("30s")
port := router.ParseValueDefault(os.Getenv("PORT"), 8080)
```

An empty string yields the zero value of `T` and no error, as it does in a bound
struct.

`QueryAllAs[T]` reads a repeated query parameter:

```go
tags, err := c.QueryAllAs[string]("tag")   // ?tag=go&tag=http
```

The form accessors read the posted body **alone** and never the query string, so
a parameter in the URL cannot forge a field of the form:

```go
name := c.FormValue("name")
kind := c.FormDefault("kind", "note")
age, err := c.FormAs[int]("age")
vals, err := c.FormValues()

f, fh, err := c.FormFile("avatar")       // the caller closes f
files, err := c.FormFiles("attachment")  // one field, several files
form, err := c.MultipartForm()
```

`FormValue` and `FormDefault` swallow the error of a body that does not parse,
the way `Query` has none to report. Use `FormValues` to see it.

Request bodies are capped at 4 MiB. Change it with `r.MaxBodyBytes(n)`, and the
memory a multipart body may use before it spills to disk with
`r.MaxMultipartMemory(n)`.

> `encoding/json/v2` refuses to encode a `time.Duration` without a format tag.
> Write `json:"ttl,format:nano"` or use a string.

## Templates

`Render` writes an HTML body from a component:

```go
type Component interface {
	Render(ctx context.Context, w io.Writer) error
}
```

That is the interface a [templ](https://templ.guide) template satisfies, so a
generated template goes straight to `Render` and the router still imports
nothing:

```go
r.GET("/posts/{slug}", func(c *app.Context) error {
	post, err := c.DB.Post(c, c.Param("slug"))
	if err != nil {
		return err
	}
	return c.Render(http.StatusOK, view.Post(post))   // view.Post is a templ.Component
})
```

`Render` builds the page in a pooled buffer before it writes the header, the
way `JSON` does. A template that fails halfway therefore produces a clean 500
instead of half a page, and the response carries a `Content-Length`.

An error from the template reaches the error handler. An `HTTPError` keeps its
status, so a template that reports `router.ErrNotFound` answers 404; any other
error becomes a 500 whose message stays server-side.

The buffer pool starts each page at 8 KiB and keeps a buffer up to 512 KiB. A
page larger than that still renders, but its buffer is dropped instead of
pooled, so a service whose pages routinely exceed 512 KiB allocates one per
request.

### Reading the request from a template

The component receives the context itself. `Set` values arrive through
`ctx.Value`, and `FromContext` returns the whole `Base`, which is what a nav bar
needs to mark the active link:

```templ
templ Nav() {
	if c, ok := router.FromContext(ctx); ok {
		<a href="/docs" aria-current={ ariaCurrent(c.Path() == "/docs") }>Docs</a>
	}
}
```

`FromContext` answers through `Value`, so it still finds the `Base` after the
templ runtime wraps the context. It reports `false` outside a request, which
keeps a template renderable from a test or a static site generator.

It returns the request state, not your `*app.Context`. A template that needs
the user or the database takes it as a parameter — `view.Post(post, user)` —
which keeps the value typed and the template testable. Reaching for
`c.Get("user").(*User)` inside a template gives back the type assertion the
router exists to remove.

> The context is only valid until `Render` returns. A pooled router reuses it
> for the next request, so a template must not hold on to it, and must not hand
> it to a goroutine that outlives the request.

### Streaming

`RenderStream` writes straight to the client, with no buffer. Use it for a page
too large to hold in memory, or for one that sends its shell early:

```go
return c.RenderStream(http.StatusOK, view.Feed(items))   // @templ.Flush() reaches the client
```

It commits the response before the template runs, so a failure leaves a partial
page on the wire. The error still reaches the error handler, which logs it and
writes nothing more; a write that failed because the client went away is logged
at debug level rather than as a 500.

A `HEAD` request answers with the headers alone and never runs the template, so
a streamed route sends no `Content-Length`. `Render` runs the template for a
`HEAD` and sends the length.

### Fragments and other templ helpers

`ComponentFunc` adapts any function of that shape, which reaches the parts of
templ that the interface alone does not cover. Rendering named fragments for an
htmx request is the common one:

```go
r.GET("/rows", func(c *app.Context) error {
	return c.Render(http.StatusOK, router.ComponentFunc(
		func(ctx context.Context, w io.Writer) error {
			return templ.RenderFragments(ctx, w, view.Table(rows), "rows")
		}))
})
```

### An HTML error page

The error handler renders a component like any other handler. It replaces
`DefaultErrorHandler` wholesale, so it has to keep the logging, and it has to
answer even when the error page itself fails to render:

```go
r.ErrorHandler(func(c *Context, err error) {
	status := router.StatusOf(err)
	if status >= 500 {
		slog.ErrorContext(c, "request failed",
			slog.String("route", c.RoutePattern()), slog.Any("error", err))
	}
	if c.Response().Committed {
		return
	}
	if renderErr := c.Render(status, view.Error(status)); renderErr != nil {
		slog.ErrorContext(c, "error page failed", slog.Any("error", renderErr))
		//nolint:errcheck // the error handler is the last stop
		c.String(status, http.StatusText(status))
	}
})
```

Without the fallback a broken error template writes nothing, and the client
gets an empty `200` for what was a 500.

Keep the JSON handler for an API scope and mount the HTML one under the pages
scope; see [Mounting a subsystem with its own context](#mounting-a-subsystem-with-its-own-context).

## htmx

htmx turns a link or a form into a request that swaps a fragment of HTML into
the page. The router reads the headers that it sends and writes the headers
that steer it, so a handler never spells one out.

[`_examples/chat`](_examples/chat) is a chat room built from `html/template`,
htmx and one event stream. Run it with `go run .` in that directory.

### Reading the request

```go
c.IsHTMX()     // htmx made this request
c.IsBoosted()  // it came from an element under hx-boost
c.HTMX()       // every htmx header, as a struct
```

`HTMX` returns the whole set: `Request`, `Boosted`, `HistoryRestore`,
`CurrentURL`, `Prompt`, `Target`, `Trigger` and `TriggerName`. Every field is
empty for a request that htmx did not make.

`HTMXPartial` gives one URL two answers, a fragment and the page around it:

```go
r.GET("/messages", router.HTMXPartial(messageList, messagePage))
```

A boosted request gets the page, not the fragment: htmx takes the body of that
answer and swaps the whole of it, so a fragment would drop the rest of the
document. A history restore request gets the page for the same reason. The
handler also adds `HX-Request` and `HX-Boosted` to `Vary`, because the URL now
has two answers and a shared cache has to keep them apart. Branching on
`c.IsHTMX()` yourself means calling `c.Vary(router.HeaderHXRequest)` yourself.

### Writing the response

`c.HX()` opens a chain. Each method sets one header and returns the chain, and
the last one writes the body:

```go
return c.HX().
	Retarget("#row-7").
	Reswap(router.HXSwapOuterHTML).
	Trigger("row-saved").
	Render(http.StatusOK, view.Row(row))
```

| Header method | Asks htmx to |
| --- | --- |
| `PushURL(url)` | push `url` onto the history stack |
| `ReplaceURL(url)` | replace the entry in the address bar |
| `Retarget(sel)` | swap into another element |
| `Reselect(sel)` | swap only a part of this answer |
| `Reswap(swap)` | swap another way, such as `router.HXSwapBeforeEnd` |
| `Refresh()` | load the page again |
| `Trigger(names...)` | fire client-side events |
| `TriggerAfterSwap`, `TriggerAfterSettle` | fire them later |
| `TriggerEvents(events...)` | fire events that carry a payload |

| Body method | Writes |
| --- | --- |
| `Render`, `RenderStream`, `HTML`, `String`, `JSON`, `NoContent` | what `Base` writes |
| `NoSwap()` | `204`, which leaves the page alone |
| `Redirect(url)` | `HX-Redirect`, or a `303` for a client that is not htmx |
| `Location(path)`, `LocationWith(loc)` | `HX-Location`, which navigates without a full load |

A chain that fails carries the failure to the method that ends it, so the
handler returns it like any other error. Read it with `Err()` when the chain
ends on a header method instead.

### The redirect

htmx follows a `303` inside the request that it made, and swaps whatever the
new location answers into the element that asked. That is almost never what a
redirect after a form post means, so `Redirect` writes `HX-Redirect` for an
htmx request and a plain `303` for anything else:

```go
r.POST("/join", func(c *app.Context) error {
	...
	return c.HX().Redirect("/chat")   // HX-Redirect for htmx, 303 for a browser
})
```

`Location` and `LocationWith` answer the same way, with the client-side
navigation that keeps the page and its scripts alive.

To convert every redirect of a scope instead of writing `HX()` in each handler,
mount `middleware.HTMXRedirect` on the pages:

```go
pages.Use(middleware.HTMXRedirect[Ctx])   // a 3xx with a Location becomes HX-Redirect

pages.POST("/join", func(c *app.Context) error {
	...
	return c.Redirect(http.StatusSeeOther, "/chat")   // htmx gets HX-Redirect
})
```

Three kinds of request pass through untouched, because each one wants the
redirect that the handler wrote: a request htmx did not make, a boosted
request, which htmx follows itself and swaps as the new page, and a history
restore request. Mount it on the scope that serves pages and never on an API
scope, where a `3xx` still has to mean "the resource is elsewhere".

`HTMXRedirectConfig{Location: true}` writes `HX-Location` instead, which
navigates without a full page load. The middleware and `HX()` compose: a
handler that calls `c.HX().Redirect` writes no `Location`, so there is nothing
left to rewrite.

### An answer that swaps nothing

`NoSwap` writes a `204`, which tells htmx to leave the page alone. The headers
of the chain still apply, so it is the answer for a request whose result
reaches the page by another road:

```go
return c.HX().Trigger("message-sent").NoSwap()
```

```html
<form hx-post="/messages" hx-on:message-sent="this.reset()">
```

### Events with a payload

`TriggerEvents` writes the JSON form of the trigger header, which reaches a
listener as `event.detail`:

```go
c.HX().TriggerEvents(
	router.HXEvent{Name: "toast", Detail: "Saved"},
	router.HXEvent{Name: "rows-changed", Detail: map[string]int{"total": 7}},
)
```

A browser reads a header one byte per character, so the header escapes every
character outside ASCII. `Trigger` writes the plain comma separated form and
reports a name that the header cannot carry.

### Fragments over a stream

`SendComponent` renders a template into an event, which is what the sse
extension of htmx swaps:

```go
r.GET("/events", func(c *app.Context) error {
	ch, unsubscribe := c.Room.Join()
	defer unsubscribe()

	return router.ServeSSE(c, ch, router.SSEComponent("message", view.Message),
		router.SSEHeartbeat(20*time.Second))
})
```

```html
<div hx-ext="sse" sse-connect="/events">
	<div id="log" sse-swap="message" hx-swap="beforeend"></div>
</div>
```

See [Server-sent events](#server-sent-events) for the rest of the stream API.

### Errors

`DefaultErrorHandler` answers an htmx request with the message as escaped HTML
rather than as JSON, because htmx sends `Accept: */*` and swaps what it
receives into a page. A request that names JSON in its `Accept` header still
gets JSON.

htmx swaps no `4xx` or `5xx` by default, so the body only shows on a page that
configures `responseHandling` or uses the response-targets extension. Render a
real error fragment with an error handler of your own; see
[An HTML error page](#an-html-error-page).

### Testing

`routertest.HTMX()` marks a request as one that htmx made:

```go
routertest.Get(r, "/messages", routertest.HTMX()).AssertBody(t, "fragment")
routertest.Get(r, "/messages").AssertBody(t, "the whole page")
```

## Files and downloads

```go
c.File("invoices/2026-01.pdf")                       // from the working directory
c.FileFS("dist/app.js", assets)                      // from an embed.FS
c.AttachmentFile("exports/q1.csv", "q1-report.csv")  // the browser saves it
c.InlineFile("invoices/7.pdf", "invoice-7.pdf")      // the browser opens it
```

The name is a slash-separated path inside the working directory. Every `..` comes
out of it before the open and `os.Root` resolves what is left, so a name that came
out of a request reaches no file outside that directory, not even through a
symbolic link. A file the process cannot open answers `ErrNotFound`, and so does a
directory; the reason reaches the log and never the client, because the layout of
the disk is no answer to a request.

```go
r.GET("/invoices/{id}", func(c *app.Context) error {
	if !c.User.Owns(c.Param("id")) {
		return router.ErrForbidden
	}
	return c.File("invoices/" + c.Param("id") + ".pdf")
})
```

The answer goes out through `http.ServeContent`, which sets the media type from
the extension, answers a range request with 206 and a conditional one with 304.
The disposition header goes out only once the file is open, so a miss answers a
plain 404 that no browser saves to disk.

`Attachment` and `Inline` take a body the process already holds in memory.
`AttachmentFile` and `InlineFile` stream from disk instead, so a large export
costs no copy per request and a download that lost the connection resumes.

## Signed cookies and flash messages

A `CookieCodec` signs a cookie value with HMAC-SHA256, so the server can tell that
it wrote it:

```go
codec := router.NewCookieCodec(key)   // key from crypto/rand, 32 bytes or more

c.SetSignedCookie(codec, &http.Cookie{
	Name:     "uid",
	Value:    user.ID,
	Path:     "/",
	MaxAge:   int(7 * 24 * time.Hour / time.Second),
	Secure:   true,
	HttpOnly: true,
	SameSite: http.SameSiteLaxMode,
})

id, err := c.SignedCookie(codec, "uid")
```

The value **stays readable** by the client; the signature only proves the server
wrote it. Encrypt anything the client must not read, or keep it on the server and
put an identifier in the cookie.

The signature covers the cookie name and an expiry as well as the value. A value
therefore verifies under the name that signed it and under no other, which keeps
it from moving out of one cookie into another, and a signature stops verifying
once its expiry passes. `SetSignedCookie` takes that expiry from the cookie's own
`MaxAge`, then `Expires`, then `CookieCodec.MaxAge`, so the browser never holds a
cookie that stopped verifying.

`NewCookieCodec` panics on a key shorter than `MinCookieKeyLen`, 32 bytes: a short
key is almost always a password that reached the argument in place of a key, and a
panic at start-up names that where a weak signature never does.

### Flash messages

A flash is a message a handler leaves for the next request, the way a POST that
succeeds leaves "saved":

```go
if err := c.AddFlash(codec, router.Flash{Kind: "error", Message: "that name is taken"}); err != nil {
	return err
}
return c.Redirect(http.StatusSeeOther, "/signup")
```

```go
return c.Render(http.StatusOK, view.Signup(c.Flashes(codec)))
```

`Flashes` reads and clears in one call, which is the contract that makes a flash a
flash: a message reaches one page and no more. A second call answers with nothing.
Both it and `AddFlash` add `Vary: Cookie`, so a shared cache cannot hand one user
the messages of another.

The messages live in a signed cookie and nowhere else. This is not a session
package: it keeps no server-side store, mints no session identifier and collects
nothing, and a message the browser drops is gone. That buys a server holding no
state between two requests, and it costs room — `AddFlash` reports
`ErrFlashTooLarge` once the cookie passes `MaxCookieSize`, 4096 bytes, and leaves
the cookie already written in place. A flash carries a sentence and a kind, never
a form.

Call both before the handler writes the body. A header that a committed response
gains never reaches the client.
## Server-sent events

`ServeSSE` streams the values of a channel to the client. A sender turns each
value into events, so the channel carries your own type and never an encoded
one:

```go
r.GET("/notifications", func(c *app.Context) error {
	ch, unsubscribe := c.Bus.Subscribe(c.User.ID)
	defer unsubscribe()

	return router.ServeSSE(c, ch, router.SSEJSON[Notification]("notification"),
		router.SSEHeartbeat(15*time.Second))
})
```

The handler blocks until the channel closes or the client goes away, and
returns `nil` for either, because neither is a failure. It starts no goroutine,
so a pooled context stays safe.

Four senders cover the usual payloads:

| Sender | Writes |
| --- | --- |
| `router.SSEJSON[T]("name")` | the value as JSON |
| `router.SSEText[T]("name")` | the value through `fmt.Sprint` |
| `router.SSEComponent("name", view)` | the value as HTML, from a templ component |
| `router.SSEEvents()` | a channel that already carries `router.Event` |

Write your own for anything else, or to send several events for one value:

```go
func send(s *router.SSEWriter, p Post) error {
	if err := s.SendComponent("post", view.PostCard(p)); err != nil {
		return err
	}
	return s.SendJSON("count", p.Comments)
}
```

### One stream, many routes

`NewSSEStream` holds the sender and the options, so a route declares the shape
of its events once. The value carries no request state, so it lives at package
level and every request reuses it:

```go
var feed = router.NewSSEStream(
	router.SSEComponent("post", view.PostCard),
	router.SSEHeartbeat(15*time.Second),
	router.SSERetry(3*time.Second),
)

r.GET("/feed", func(c *app.Context) error {
	ch, unsubscribe := c.Bus.Posts(c.User.ID)
	defer unsubscribe()
	return feed.Serve(c, ch)
})
```

### Options

| Option | Effect |
| --- | --- |
| `SSEHeartbeat(d)` | sends `: ping` every `d`, which keeps a proxy from closing a connection that carries no event for a while |
| `SSERetry(d)` | asks the client to wait `d` before it reconnects |
| `SSEClose(e)` | sends `e` when the channel closes, which tells the client that the stream is over |

Only a driver honours them, because only a driver owns the loop.

### Writing the loop yourself

`c.SSE` commits the response and hands back the writer. Reach for it when the
events come from somewhere other than one channel:

```go
r.GET("/progress/{id}", func(c *app.Context) error {
	s, err := c.SSE(http.StatusOK)
	if err != nil {
		return err
	}

	for _, step := range c.Jobs.Steps(c.Param("id"), c.LastEventID()) {
		if err := s.Send(router.Event{ID: step.ID, Name: "step", Data: step.Text}); err != nil {
			return err
		}
	}
	return nil
})
```

`Send`, `SendData`, `SendJSON`, `SendComponent` and `Comment` each write a whole
frame and flush it. A write or a flush that fails closes the stream, and every
later send reports the same failure, so a loop that watches its errors ends. An
event that the writer rejects before it writes anything — a line break in `ID`
or `Name`, a value that fails to encode, a component that fails — leaves the
stream open, because nothing reached the client.

`SendJSON` encodes straight into the frame and `SendComponent` renders into it,
so a value that fails to encode, or a template that fails halfway, leaves
nothing on the wire.

A line break inside `Data` becomes a data line of its own, so a multiline
payload stays one event. A line break inside `ID` or `Name` is a bug — it would
let the value forge events of its own — so the writer reports it and writes
nothing.

### Reconnects

A browser reconnects by itself after any end of the stream, and sends the `ID`
of the last event it saw in `Last-Event-ID`. Read it with `c.LastEventID()` and
replay what the client missed.

### What the stream sets

`text/event-stream`, `Cache-Control: no-cache`, `X-Accel-Buffering: no`, and
`Connection: keep-alive` on HTTP/1. It also clears the write deadline of the
connection, because a stream outlives any `Server.WriteTimeout`, and it flushes
the header at once, so the `EventSource` of a browser fires its open event
before the first event arrives.

> Skip the middleware that a long stream does not survive: `middleware.Timeout`
> cuts it at its deadline, and a compression wrapper buffers the events until
> the client sees none. Give each of them a `Skip` for the route, or apply them
> to a scope that leaves the stream out.

## Middleware

```go
type Middleware[C Context] func(next HandlerFunc[C]) HandlerFunc[C]
```

Every middleware comes in two forms. The plain one is a `router.Middleware[C]`
itself, with the default config, so it goes into `Use` without a call; the
`WithConfig` one is a factory that takes a config:

```go
r.Use(middleware.Recover[Ctx])
r.Use(middleware.RequestID[Ctx])
r.Use(middleware.RealIP[Ctx])

r.Use(middleware.LoggerWithConfig[Ctx](middleware.LoggerConfig{Logger: log}))
r.Use(middleware.CORSWithConfig[Ctx](middleware.CORSConfig{
	AllowOrigins: []string{"https://app.example"},
}))
r.Use(middleware.TimeoutWithConfig[Ctx](middleware.TimeoutConfig{
	Duration: 5 * time.Second,
}))
```

The context type has to be written at the call site: Go infers a type argument
from the arguments of a call, and these calls carry nothing that names the
context. One type alias takes the repetition out of it:

```go
type Ctx = *app.Context
```

Every config carries a `Skip`. Return true from it and the request goes
straight to the next handler:

```go
middleware.LoggerWithConfig[Ctx](middleware.LoggerConfig{
	Skip: func(c router.Context) bool { return c.Request().URL.Path == "/health" },
})
```

`Skip` takes the `router.Context` interface, not your context type, so one
function fits any router. It reaches the request through `c.Request()` and the
matched route through `c.RoutePattern()`.

One middleware per file, named after it:

| | | stage |
|---|---|---|
| `Recover` | turns a panic into a 500, inside the chain | `Use` |
| `RequestID` | reads or mints `X-Request-Id`, readable with `RequestIDFrom` | `Use` |
| `RealIP` | rewrites `RemoteAddr` from the forwarding header you name, and deletes the rest | `Use` |
| `Logger` | one `log/slog` record per request | `Use` |
| `Timeout` | a deadline on the request context | `Use` |
| `CORS` | the cross-origin headers and the preflight answer | `Use` |
| `Secure` | the security headers of an answer | `Use` |
| `CSRF` | refuses a request another site made the browser send | `Use` |
| `KeyAuth` `BasicAuth` | authentication, onto a typed field of your context | `Use` |
| `RateLimit` | a token bucket per client | `Use` |
| `BodyLimit` | caps the request body itself | `Use` |
| `Decompress` | expands a `Content-Encoding: gzip` request body | `Use` |
| `Gzip` | compresses the response body | `Use` |
| `Rewrite` | rewrites the request path | `Pre` |
| `MethodOverride` | turns a POST into the method the request names | `Pre` |

`Rewrite` and `MethodOverride` belong in `Pre`, the stage that runs before
matching. In `Use` the path and the method have already picked the route, and
changing them there changes nothing.

Two defaults are worth knowing: `CORS[Ctx]` allows every origin without
credentials, and `Timeout[Ctx]` applies a deadline of `DefaultTimeout`, 30
seconds. `HTMXRedirect[Ctx]` belongs on a page scope only; see
[the redirect](#the-redirect).

Standard `func(http.Handler) http.Handler` middleware works through an adapter:

```go
r.Use(router.WrapMiddleware[*Context](gziphandler.GzipHandler))
r.GET("/metrics", router.WrapHandler[*Context](promhttp.Handler()))
```

`Timeout` puts a deadline on the request context and does not abandon a running
handler, so your handler has to watch the context. `TimeoutWithConfig` **panics
on a `Duration` of zero or less**: a middleware that reads a zero as "no
deadline" answers a misconfigured server with a server that waits forever, which
is the failure it exists to prevent. Use `Timeout` for the default, and `Skip` to
leave a route out. `OnTimeout` replaces the status and the message, and receives
the handler's error joined with `context.DeadlineExceeded`.

`CORSWithConfig` **panics at construction** on an `AllowOrigins` entry that is not
a bare scheme and host, and on `"*"` together with `AllowCredentials`. An origin
with a path or a trailing slash matches no request that ever arrives, so it is a
typo that would otherwise stay silent until a client hit it; `"*"` with
credentials would let every site read the answers of a signed-in user. Reach
anything else through `AllowOriginFunc`.

## CSRF and security

### The security headers

`Secure` writes them before the handler runs, so an answer the handler wrote and
an answer an error produced carry the same ones:

| | default |
|---|---|
| `X-Content-Type-Options` | `nosniff` |
| `X-Frame-Options` | `SAMEORIGIN` |
| `Referrer-Policy` | `strict-origin-when-cross-origin` |
| `Content-Security-Policy` | none, because a policy that fits every application does not exist |
| `Strict-Transport-Security` | none |

Assign `middleware.SecureOmit` to a field to turn one header off; a page that
names its `frame-ancestors` in a policy has no use for `X-Frame-Options`.
`CSPReportOnly` sends the policy as `Content-Security-Policy-Report-Only`, which
reports what it would have blocked and blocks nothing, so a new policy can be
measured before it takes effect.

Set `HSTSMaxAge` only once the site is ready to serve every path over TLS
forever: a browser that saw the header refuses plaintext until it expires, and no
answer can take that back.

It does not send `X-XSS-Protection`. Every current browser ignores it, and the
filter it used to turn on introduced holes of its own.

### CSRF

`CSRF` reads `Sec-Fetch-Site` first. Every current browser sends that header,
sets it from the request itself and lets no page touch it, so a request it labels
`same-origin` or `none` passes. A request it labels anything else passes only when
its `Origin` is one of `TrustedOrigins`.

A client that sends no such header falls back to the double-submit cookie: the
middleware writes a random token to a cookie, and an unsafe request has to send
the same token back in a header or a form field. A page on another origin makes
the browser send the cookie, but the same-origin policy keeps it from reading the
value.

The token cookie is `Secure` on a request that arrived over TLS, so the defaults
suit a server-rendered application behind TLS termination. Set `CookieSecure` to
force the attribute on every answer, and `CookieHTTPOnly` for a page that renders
the token into its forms rather than reading the cookie from a script:

```go
r.Use(middleware.CSRFWithConfig[Ctx](middleware.CSRFConfig{
	CookieHTTPOnly: true,
}))
```

A safe method gets the cookie and the token and skips both checks, which is what
lets a page render the token into its forms:

```go
<input type="hidden" name="_csrf" value={ middleware.CSRFTokenFrom(c) }>
```

Every answer it handles carries `Vary: Cookie`, so a shared cache cannot hand one
user the token of another. A malformed `TrustedOrigins` entry panics at
construction, for the same reason `CORS` does.

### Authentication

Both validators run on **your** context type, so they write the caller they
identified onto a typed field and the handler reads it without an assertion:

```go
r.Use(middleware.KeyAuth(func(c Ctx, key string) (bool, error) {
	caller, err := app.Callers.ByKey(c, key)
	if errors.Is(err, app.ErrNoCaller) {
		return false, nil          // 401
	}
	if err != nil {
		return false, err          // the status of that error
	}
	c.Caller = caller
	return true, nil
}))
```

The validator names the context type, so these two calls need no type argument.
`KeyAuth` reads a bearer token from `Authorization` by default; point it at other
places with `FromHeader`, `FromQuery`, `FromCookie` and `FromForm`. `BasicAuth`
does RFC 7617 and sends the `WWW-Authenticate` challenge that makes a browser
prompt. A config without a validator panics at construction.

Compare a secret the application holds in memory with `middleware.SecureCompare`
and never with `==`: a comparison that returns at the first byte that differs
turns a search of the whole key space into a search of one byte at a time.

### Rate limits and body limits

```go
r.Use(middleware.RateLimit(middleware.NewMemoryStore[Ctx](10, 30, time.Minute)))
```

`NewMemoryStore` is a token bucket per client: a rate per second, a burst, and how
long an idle visitor is kept. `RateLimitStore` takes the application context, so a
store backed by Redis reaches the connection as the type it is. It counts against
`ClientIP` unless `KeyFunc` says otherwise, and a denial sets `Retry-After`. The
wait is reported without taking a token, so a client that keeps knocking while it
is denied does not push its own answer further away.

`BodyLimit` caps the request body itself, which is what a handler that reads
`Request.Body` or streams an upload needs; `r.MaxBodyBytes` caps only what `Bind`
reads. Both fit together, and a body that runs past either reports
`ErrPayloadTooLarge`.

`Decompress` expands a `Content-Encoding: gzip` request body and caps the result
at `DefaultMaxDecompressedSize`, 100 MiB. That cap is the one that counts once a
body expands: a few hundred bytes of zeros become megabytes, so put `BodyLimit`
above it to cap both halves.

### RealIP and the trust set

> **This is a change of behaviour.** `RealIP` used to take the **leftmost** entry
> of the forwarding header. That entry is the one the client writes, so anyone
> could send `X-Forwarded-For: 1.2.3.4` and become that address in the rate
> limiter, the audit log and the geo check.

It now walks the chain from the server outwards — the last entry first, the
address of the connection past its end — and stops at the first hop outside the
trust set. That hop is the nearest address the client could not forge. A
connection no trusted proxy made keeps its own address, and every forwarding
header it carries is deleted, `X-Forwarded-Proto` included, so nothing after the
middleware reads one either.

`Headers` has **no default**. Name the forwarding header your own proxies write,
and nothing else:

```go
middleware.RealIPWithConfig[Ctx](middleware.RealIPConfig{
	Headers: []string{router.HeaderXForwardedFor},
})
```

nginx, HAProxy, an AWS or GCP load balancer, Cloudflare and Envoy all append to
`X-Forwarded-For`, and none of them writes or strips the `Forwarded` header of
RFC 7239. Naming a header your proxy does not write makes that header
client-controlled: the proxy relays it untouched and the client picks whatever
address it likes. Every forwarding header you do not name is deleted from the
request. `RealIP` with no config names none, so it reads none and the address of
the connection stands.

The default trust set holds the loopback, private and link-local ranges, which is
where a proxy sits in a deployment that has one. Name the range your load balancer
occupies instead:

```go
middleware.RealIPWithConfig[Ctx](middleware.RealIPConfig{
	Trust: middleware.NewTrustSet(
		middleware.TrustLoopback(false),
		middleware.TrustPrivateNet(false),
		middleware.TrustLinkLocal(false),
		middleware.TrustPrefix(netip.MustParsePrefix("203.0.113.0/24")),
	),
})
```

Set `Leftmost: true` to bring the old reading back. Do that **only** where the
proxy in front replaces the header outright rather than appending to it.

A named `Forwarded` header reports the protocol too, and the `proto` of the hop
that stands goes into `X-Forwarded-Proto`, where `Base.Scheme` and `Secure` read
the scheme of the request.

`X-Forwarded-Proto` is a forwarding header like the others: it survives only
while `Headers` names it, on a trusted connection as on any other. A trusted peer
says a proxy made the connection, not that the proxy wrote this header, and an
ingress that appends an address without setting a protocol leaves it to the
client. The scheme decides whether a cookie is `Secure`, whether the answer
carries HSTS and what an absolute URL points at, so name the header for a
deployment that terminates TLS at a proxy:

```go
middleware.RealIPWithConfig[Ctx](middleware.RealIPConfig{
	Headers: []string{router.HeaderXForwardedFor, router.HeaderXForwardedProto},
})
```

### Compression, rewrites and method override

`Gzip` compresses the response body when the client accepts it, from
`DefaultGzipMinLength`, 1024 bytes, up. It sends `Vary: Accept-Encoding` on every
answer, compressed or not, because a cache that stores one answer under a key that
does not name the encoding serves it to a client that cannot read it. A
`text/event-stream` answer passes through uncompressed, because an event sitting in
a compression buffer reaches the client at the end of the stream instead of at
once.

The wrapper sits **outside** `Response`, so `Response.Size` counts the compressed
bytes that reached the client rather than the bytes the handler wrote. That is the
number an access log wants; `Response.Status` is the status either way.

`Rewrite` rewrites the path. `*` is the only wildcard, so a rule reads as the paths
it rewrites, and `$1` to `$9` hold what each took:

```go
r.Pre(middleware.Rewrite[Ctx](
	middleware.RewriteRule{Match: "/api/v1/*", To: "/v1/$1"},
	middleware.RewriteRule{Match: "/legacy", To: "/"},
))
```

The rules are an ordered slice and the first match wins; the path is rewritten
once and never fed back through, so two rules cannot chain.

`MethodOverride` is how an HTML form reaches `DELETE` without a line of
JavaScript. It only ever upgrades a POST, and only to PUT, PATCH or DELETE: a POST
that turned into a GET would leave a request a cache stores and a proxy repeats.

```go
r.Pre(middleware.MethodOverrideWithConfig[Ctx](middleware.MethodOverrideConfig{
	Getter: middleware.MethodFromForm("_method"),
}))
```

## Static assets

The `static` package serves the files of a front-end build and writes their
URLs into a template.

```go
//go:embed all:dist
var dist embed.FS

assets := static.Must(static.Config{
	FS:     dist,
	Root:   "dist",        // //go:embed keeps the directory name in the paths
	Prefix: "/static",
})

static.Mount(r, assets)          // GET and HEAD under /static
tmpl.Funcs(assets.FuncMap())     // {{ asset "css/app.css" }}
```

`assets.URL("css/app.css")` returns `/static/9f2c1ab40e3d/css/app.css`. The
middle segment is the build tag, which `New` derives from the content of the
files. A path that carries it answers `Cache-Control: public, max-age=31536000,
immutable`, so the browser keeps the file until the next build changes the tag.
A path without the tag still answers, with a revalidating header instead, and
the tag of an older build answers 404.

### Two modes

| | `Config.FS` | `Config.Dir` |
|---|---|---|
| source | an embedded file system | a directory on disk, read per request |
| build tag | derived from the content | none |
| `Cache-Control` | immutable for a versioned path | `no-cache` |
| `ETag` | the content hash, read once at startup | the size and the modification time |

`Dir` opens the file again on every request, so an edit reaches the next
reload, and `os.OpenInRoot` refuses a path that leaves the directory. Pick the
mode with a build tag of your own, so that one call site serves both builds:

```go
//go:build dev

func assetConfig() static.Config {
	return static.Config{Dir: "web/dist", Prefix: "/static"}
}
```

```go
//go:build !dev

//go:embed all:dist
var dist embed.FS

func assetConfig() static.Config {
	return static.Config{FS: dist, Root: "dist", Prefix: "/static"}
}
```

`go run -tags dev .` then reads the disk, and the release build embeds.

### Single page application

Leave `Prefix` empty and set `SPA`:

```go
assets := static.Must(static.Config{FS: dist, Root: "dist", SPA: true})

r.GET("/api/ping", ping)
static.Mount(r, assets)
```

A path that matches no file answers with `index.html` and status 200, which is
how the application serves its own routes. The catch-all that `Mount` registers
loses to every literal route, so `/api/ping` still reaches its handler.

The fallback answers a navigation only: the path carries no file extension, or
the client accepts `text/html`. A missing script keeps its 404, because an HTML
body in its place breaks the page in a way that is hard to read. Set
`Config.Fallback` to replace that test when the routes of the application do not
fit it. The index itself always revalidates, whatever path reached it, because
it names the versioned assets.

### The rest of the API

| | |
|---|---|
| `a.URL(name)` | the public URL of one asset, with the build tag |
| `a.FuncMap()` | `{"asset": a.URL}`, for `text/template` and `html/template` |
| `a.Has(name)` | whether the set holds the file |
| `a.Prefix()` `a.Build()` | the normalized prefix and the build tag |

`Assets` is an `http.Handler` as well, so `r.MountHandler("/static", assets)`
and `http.StripPrefix` serve it too. `Mount` differs in two ways: it registers
`GET` and `HEAD` only, and a miss returns `router.ErrNotFound`, which the
`NotFound` handler or the error handler of the router renders. `MountHandler`
answers a miss from `Config.NotFound` instead.

`Config.RedirectDir` answers a request for a directory whose URL carries no
trailing slash with a 301 to the same path and a slash. `/docs` and `/docs/` are
different bases for a relative link, so an index writing `assets/app.css` reaches
the wrong file under the unslashed one. The `Location` is relative, so it keeps
the prefix that `StripPrefix` or `MountHandler` removed before this package saw
the path. It answers only for a directory that holds the index, which is what
keeps it clear of the SPA fallback. It is opt-in.

## Running the server

The `serve` package runs an `http.Server` that stops when its context does:

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()

if err := serve.Run(ctx, r, serve.Config{Addr: ":8080"}); err != nil {
	log.Fatal(err)
}
```

`Run` binds the address, serves the handler, and drains the requests still in
flight once the context is cancelled. It blocks for as long as the server runs and
returns **nil** for the shutdown the context asked for; every other end is an
error, be it an address in use, a certificate that does not load, or a drain that
ran out of time. A context already done when `Run` is called serves nothing and
returns nil, so a server starting inside a group whose context has failed reports
no failure of its own.

The package takes an `http.Handler`, so it never sees the context type of the
router, and it serves a router, a mounted subsystem or any standard handler.

| | |
|---|---|
| `Addr` `Network` | the address to bind; `Listener` replaces both |
| `Listener` | a socket the process inherited, or one a test bound |
| `TLSConfig` | used as it stands; the cert options only add to it |
| `ReadHeaderTimeout` | the one deadline with a default, 10s; negative removes it |
| `ReadTimeout` `WriteTimeout` `IdleTimeout` | no defaults |
| `ShutdownTimeout` | how long the drain may take, 10s; negative closes at once |
| `Logger` | what the server reports on its own, at error level |
| `OnListen` | the bound address, once, before the first request |
| `OnServer` | the `*http.Server` before it binds, for the rest of its fields |

`ReadHeaderTimeout` is the one deadline the package sets for you, because it covers
the header phase alone: no client needs seconds to write its headers, however long
its body or the answer takes. `WriteTimeout` is the field to reach for last — it
covers the whole response, so it cuts a long download, a slow report and an SSE
stream off in the middle, and the client reads a truncated body rather than an
error.

The drain waits for the running handlers to return and **does not cancel their
request contexts**, which is what makes it graceful: a request halfway through a
transaction finishes it. A handler that runs until something tells it to stop, such
as a stream or a long poll, therefore holds the drain until the timeout unless it
has a signal of its own — `RegisterOnShutdown` from `OnServer` is that signal:

```go
serve.Config{
	OnServer: func(s *http.Server) error {
		s.RegisterOnShutdown(func() { close(stopStreams) })
		return nil
	},
}
```

Serve HTTPS by passing a certificate. Several are allowed, and the handshake picks
by name and key type:

```go
err := serve.Run(ctx, r, serve.Config{Addr: ":443"}, serve.CertFiles("cert.pem", "key.pem"))
```

`CertPEM` takes blocks the caller already holds, such as ones a secret store
answered with, and `CertFS` reads them from an `embed.FS`. A certificate that fails
to load stops `Run` before it binds. Passing one of these and no `TLSConfig` gets
the defaults of the package: TLS 1.3 as the floor, and HTTP/2 offered ahead of
HTTP/1.1.

## Context pooling

`NewPooled` reuses contexts instead of allocating one per request, which
removes the single allocation a request costs.

```go
r := router.NewPooled(
	func() *app.Context { return &app.Context{DB: db} },       // no request here
	func(c *app.Context) { c.User = nil; c.Tenant = "" },      // clear your fields
)
```

Two rules, and the API enforces the first one:

1. **The factory takes no request.** A pooled context outlives the request that
   first built it. Read anything request specific in a middleware or a handler.
2. **`reset` must clear every field a handler writes.** The router clears the
   embedded `Base` itself. A field `reset` forgets carries one user's data into
   the next request, so write it as an explicit assignment of every field.

Never keep a context, its request, or its response writer alive after the
handler returns. A goroutine that outlives the request would then read and
write the context of an unrelated one. Copy what it needs before it starts.

A context whose request panicked is dropped rather than pooled, because its
state is unknown at that point.

## Testing

```go
res := routertest.Do(r, http.MethodPost, "/users", routertest.JSONBody(in))
res.AssertStatus(t, http.StatusCreated)

out, err := res.JSON[User]()

res = routertest.Get(r, "/", routertest.Host("acme.example.com"))   // a host route

srv := routertest.NewServer(t, r)   // a real server, stopped when the test ends
```

A stream that ends reads back as the events a client would parse, comments and
retry frames left out:

```go
res := routertest.Get(r, "/events")
routertest.AssertEvents(t, res,
	routertest.Event{ID: "1", Name: "tick", Data: "one"},
	routertest.Event{ID: "2", Name: "tick", Data: "two"},
)
```

`MultipartBody` posts an upload, which is what exercises `FormFile`, `FormFiles`
and `MultipartForm`. It writes the fields in name order, so one set of values
always produces the same body:

```go
res := routertest.Do(r, http.MethodPost, "/avatars",
	routertest.MultipartBody(
		url.Values{"name": {"ann"}},
		routertest.FilePart{Field: "avatar", Filename: "a.png", Content: png},
	))
```

`NewContext` builds one context of your application, along with the recorder it
answers into, so a handler runs under test without a router at all:

```go
c, rec := routertest.NewContext(t, newContext,
	routertest.WithPattern("/users/{id}"),
	routertest.WithParams(map[string]string{"id": "7"}))

if err := getUser(c); err != nil {
	t.Fatalf("getUser: %v", err)
}
if rec.Code != http.StatusOK {
	t.Fatalf("status = %d", rec.Code)
}
```

It takes the factory `router.New` takes, so the handler receives its own context
type with its own fields filled in, and it seeds the route — the one thing a
handler that reads a path parameter cannot do without. The context carries the
router's defaults for the limits a request obeys, because no router configured it:
no body limit, the multipart memory of `net/http`, and lenient binding. Parameters
are ordered by name, since a map holds no order.

`AssertGolden` compares against a file under `testdata`, and
`-routertest.update` rewrites it, which is how the output of a template becomes
the fixture later runs compare against:

```go
routertest.AssertGolden(t, "order.html", buf.Bytes())
```

```
go test ./view -routertest.update
```

Read the rewritten file before you commit it: the flag accepts whatever the code
renders, including the change that broke it. The flag carries the name of the
package, because a flag name is global to the test binary and a plain `-update`
would take the name a test package commonly declares for golden files of its own.
A binary that declares one is read too, so the two spellings do the same thing.

## Performance

Apple M3 Max, Go 1.27, one route set in the three pattern dialects. Run the
comparison yourself with `just bench-compare`, and the benchmarks of this module
alone, the host table below among them, with `just bench`.

| | static | one parameter | eight segments, three parameters |
|---|---|---|---|
| **go-router**, pooled | 46 ns, 0 allocs | 52 ns, 0 allocs | 78 ns, 0 allocs |
| **go-router** | 92 ns, 1 alloc | 97 ns, 1 alloc | 123 ns, 1 alloc |
| chi | 122 ns, 2 allocs | 216 ns, 4 allocs | 312 ns, 4 allocs |
| echo | 32 ns, 0 allocs | 39 ns, 0 allocs | 65 ns, 0 allocs |
| `http.ServeMux` | 96 ns, 0 allocs | 98 ns, 1 alloc | 261 ns, 3 allocs |

Without pooling, the one allocation is your context: route parameters land in
an array inside `Base`, so matching itself allocates nothing for up to eight
parameters. `http.ServeMux` reaches zero on a static route because it has no
context object at all — it looks the handler up and calls it with the request
the server already allocated. `NewPooled` closes that gap; echo does the same
thing by default and pays for it with the same caveats listed above.

The unpooled row is about 7 percent slower than it was before the accessors on
this page existed. `Base` grew from 368 to 408 bytes — a query cache, the parsed
form error, the logger, the multipart limit and two mode flags — which pushes the
per-request context from the 384-byte size class into the 416-byte one. That step
is the whole of it: allocating 384 bytes costs 72 ns on this machine and 416 costs
about 78. Pooling makes the size class free again, which is why the pooled row
moved by 2 to 5 percent instead.

Host routing costs the lookup that resolves it, and nothing else:

| | fixed host | `{tenant}.example.com` | `*` | a host-free route |
|---|---|---|---|---|
| **go-router** | 103 ns, 1 alloc | 105 ns, 1 alloc | 100 ns, 1 alloc | 99 ns, 1 alloc |

Matching uses a compressed radix tree, the same structure echo uses, so a
static route costs a few string comparisons rather than one lookup per
segment. Echo stays ahead by 10 to 20 percent: it carries a smaller context
and a leaner per-request setup. The rest of the gap paid for the pattern
syntax above — regular expression parameters, partial segments, and
backtracking across parameter kinds — which a plain prefix tree does not give
you.

## What Go 1.27 buys

- **Generic methods** give `c.Bind[CreateUser]()`, `c.ParamAs[int]("id")` and
  `res.JSON[User]()` — the caller names the type, no out-parameter.
- **Generic methods** also give `r.MountRouter(prefix, sub)` and
  `r.HostRouter(host, sub)`, which accept a router whose context type differs
  from the caller's.
- **Generalized function type inference** lets a bare generic function stand in
  for a `Middleware[C]`, which is how a hand-written middleware reaches
  `r.Use` without a type argument.
- **`encoding/json/v2`** is the codec: stricter, and faster to decode.
- **stdlib `uuid`** backs `middleware.RequestID`.
- **`testing/synctest`** runs the timeout and the heartbeat tests on a fake
  clock.

## Limitations

- A catch-all must span a whole segment, and two parameters cannot sit side by
  side inside one.
- A host pattern carries no port, and an IP literal host is not routable.
- Register every route before the first request. The router compiles its trie
  once and refuses a later change. The same goes for the settings: `Use`, `Pre`,
  `Name`, `Meta`, `Observe`, `Logger` and the limits all panic once the router has
  served a request.
- Pooling is opt-in, and it hands you the usual lifetime rules with it.
- **Streaming multipart is not supported.** Every form accessor parses the whole
  body first, so a handler that reads one field can no longer stream the rest.
  `http.Request.MultipartReader` streams the parts instead, and the two are
  mutually exclusive: a handler that takes the reader gets a 400 out of every
  method here that reports an error.
- **`Pre` belongs to the root router only**, because a scope cannot own a stage
  that runs before matching picks the scope. It panics on a scope.
- **`Gzip` counts compressed bytes.** The wrapper sits outside `Response`, so
  `Response.Size` is what reached the client and not what the handler wrote.
- A parameter of a `MountRouter` prefix does not cross the seam, and matching runs
  once per router there. `Mount` avoids both.
- The signed cookie is signed, not encrypted: the value stays readable by the
  client.

## Development

```bash
just check          # fmt, lint, analyze, golangci-lint, test
just fmt            # go fmt, gofumpt, goimports, betteralign
just lint           # vet, build, format check, go fix modernizers, benchmarks and examples
just analyze        # x/tools modernize, betteralign
just golangci       # golangci-lint
just test           # go test -race -cover ./...
just bench          # benchmarks of this module
just bench-compare  # against chi, echo and http.ServeMux
just vuln           # govulncheck
```

`_examples` and `benchmarks` are modules of their own, and the go tool skips a
directory whose name starts with an underscore, so `./...` reaches neither.
`just lint` builds and vets both.

CI runs the same recipes. Tool versions are pinned at the top of the
[justfile](justfile) and run through `go run tool@version`, not declared as
go.mod tools: a tool directive would put the whole linter dependency tree into
the module graph of everyone who imports this router.

`betteralign` runs in opt-in mode. Only structs marked `betteralign:check` are
reordered, which today is `Base` and `Response`, the two the router allocates
per request, and `HTMXRequest`, which a handler builds per call. Field order is
load-bearing elsewhere: reordering the JSON error body would change the wire
format.

`router.Version` is the version of the router itself, for a build that would
otherwise take a walk through `runtime/debug.ReadBuildInfo` to log it:

```go
slog.Info("starting", slog.String("router", router.Version))
```

## License

MIT. See [LICENSE](LICENSE).
