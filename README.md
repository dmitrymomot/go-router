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

func main() {
	db := openDB()

	// The factory supplies your fields. The router fills the embedded Base
	// with the request, the response and the route parameters.
	r := router.New(func(w http.ResponseWriter, req *http.Request) *Context {
		return &Context{DB: db}
	})

	r.Use(
		middleware.Recover().Middleware,
		middleware.RequestID().Middleware,
		middleware.RealIP().Middleware,
		middleware.Logger().Middleware,
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
| `Request()` `SetRequest(r)` `Response()` | the request and the response wrapper |
| `Param(name)` `ParamAs[T](name)` `ParamNames()` `RoutePattern()` | route parameters |
| `Query(name)` `QueryAs[T](name)` `QueryAsDefault(name, def)` | query parameters |
| `Bind[T]()` `BindJSON[T]()` `BindForm[T]()` `BindQuery[T]()` | request decoding |
| `JSON` `String` `Stringf` `HTML` `Blob` `Stream` `NoContent` `Redirect` `Attachment` | response rendering |
| `Set(k, v)` `Get(k)` | per-request values |
| `Deadline` `Done` `Err` `Value` | `Base` is a `context.Context` |

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
r.Any(pattern, handler, ...)               // every standard method
r.Match([]string{"GET", "POST"}, pattern, handler, ...)
```

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

`r.Routes()` returns every registered route, which is handy in a startup log or
a test that guards the route table.

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

	r.Use(middleware.Recover().Middleware)
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
	middleware.Recover().Middleware,
	middleware.RequestID().Middleware,
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
the internal cause with `log/slog`, and never puts an internal message in the
body. Replace it wholesale when you need a different shape:

```go
r.ErrorHandler(func(c *Context, err error) { ... })
r.NotFound(func(c *Context) error { ... })
r.MethodNotAllowed(func(c *Context) error { ... })
```

The router answers `405` with an `Allow` header on its own, answers `OPTIONS`
with `204` and an `Allow` header, and serves `HEAD` from the `GET` handler.

A panic never kills the connection: the router recovers it and hands it to the
error handler. `middleware.Recover` is still useful, because it catches the
panic *inside* the chain, where a logger above it still records the request.
`http.ErrAbortHandler` passes through both untouched.

## Binding

`Bind` reads the media type and dispatches; a `GET`, `HEAD` or `DELETE` reads
the query string instead of a body.

```go
in, err := c.Bind[CreateUser]()        // JSON, form or query
in, err := c.BindQuery[Filter]()       // always the query string
id, err := c.ParamAs[uuid.UUID]("id")  // any type that parses from text
page := c.QueryAsDefault("page", 1)
```

JSON uses `encoding/json/v2`, which rejects invalid UTF-8 and duplicate object
names. Set options once per router:

```go
r.JSONOptions(json.RejectUnknownMembers(true))
```

The form and query decoder reads the field name from the `form` or `query` tag,
then the `json` tag, then the field name itself. It handles strings, booleans,
numbers, `time.Duration`, anything that implements `encoding.TextUnmarshaler`
(including `time.Time`), pointers, slices, and embedded structs.

Request bodies are capped at 4 MiB. Change it with `r.MaxBodyBytes(n)`.

> `encoding/json/v2` refuses to encode a `time.Duration` without a format tag.
> Write `json:"ttl,format:nano"` or use a string.

## Middleware

```go
type Middleware[C Context] func(next HandlerFunc[C]) HandlerFunc[C]
```

Every middleware follows one shape. A function named after it takes an
optional config, fills in the defaults and returns it. That config carries a
generic `Middleware` method, and Go 1.27 infers the context type there, so you
never name your own context type. Leave the config out to take the defaults:

```go
r.Use(middleware.Recover().Middleware)
r.Use(middleware.RequestID(middleware.RequestIDConfig{IgnoreInbound: true}).Middleware)
r.Use(middleware.RealIP(middleware.RealIPConfig{Headers: []string{"Cf-Connecting-Ip"}}).Middleware)
r.Use(middleware.Logger(middleware.LoggerConfig{Logger: log}).Middleware)
r.Use(middleware.CORS(middleware.CORSConfig{AllowOrigins: []string{"https://app.example"}}).Middleware)
r.Use(middleware.Timeout(middleware.TimeoutConfig{Duration: 5 * time.Second}).Middleware)
```

Every config carries a `Skip`. Return true from it and the request goes
straight to the next handler:

```go
middleware.Logger(middleware.LoggerConfig{
	Skip: func(c router.Context) bool { return c.Request().URL.Path == "/health" },
}).Middleware
```

`Skip` takes the `router.Context` interface, not your context type, so one
function fits any router. It reaches the request through `c.Request()` and the
matched route through `c.RoutePattern()`.

Passing more than one config panics, because the middleware would have to
guess which one wins.

One middleware per file, named after it: `recover.go`, `requestid.go`,
`realip.go`, `logger.go`, `cors.go`, `timeout.go`.

Standard `func(http.Handler) http.Handler` middleware works through an adapter:

```go
r.Use(router.WrapMiddleware[*Context](gziphandler.GzipHandler))
r.GET("/metrics", router.WrapHandler[*Context](promhttp.Handler()))
```

`Timeout` puts a deadline on the request context and does not abandon a
running handler, so your handler has to watch the context. Read `RealIP`
before you trust it: use it only behind a proxy that rewrites the forwarding
headers.

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

srv := routertest.NewServer(t, r)   // a real server, stopped when the test ends
```

## Performance

Apple M3 Max, Go 1.27, one route set of 26 patterns. Run it yourself with
`cd benchmarks && go test -bench . -benchmem`.

| | static | one parameter | eight segments, three parameters |
|---|---|---|---|
| **go-router**, pooled | 41 ns, 0 allocs | 47 ns, 0 allocs | 74 ns, 0 allocs |
| **go-router** | 80 ns, 1 alloc | 84 ns, 1 alloc | 109 ns, 1 alloc |
| chi | 117 ns, 2 allocs | 207 ns, 4 allocs | 294 ns, 4 allocs |
| echo | 31 ns, 0 allocs | 39 ns, 0 allocs | 64 ns, 0 allocs |
| `http.ServeMux` | 94 ns, 0 allocs | 97 ns, 1 alloc | 251 ns, 3 allocs |

Without pooling, the one allocation is your context: route parameters land in
an array inside `Base`, so matching itself allocates nothing for up to eight
parameters. `http.ServeMux` reaches zero on a static route because it has no
context object at all — it looks the handler up and calls it with the request
the server already allocated. `NewPooled` closes that gap; echo does the same
thing by default and pays for it with the same caveats listed above.

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
- **Generic methods** also give `r.MountRouter(prefix, sub)`, which accepts a
  router whose context type differs from the caller's.
- **Generalized function type inference** is what makes
  `r.Use(middleware.Logger(cfg).Middleware)` compile without a type argument.
- **`encoding/json/v2`** is the codec: stricter, and faster to decode.
- **stdlib `uuid`** backs `middleware.RequestID`.
- **`testing/synctest`** runs the timeout test on a fake clock.

## Limitations

- A catch-all must span a whole segment, and two parameters cannot sit side by
  side inside one.
- Register every route before the first request. The router compiles its trie
  once and refuses a later change.
- Pooling is opt-in, and it hands you the usual lifetime rules with it.

## License

MIT. See [LICENSE](LICENSE).
