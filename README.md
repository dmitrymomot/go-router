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

	r.Use(middleware.Recover, middleware.RequestID, middleware.RealIP)
	r.Use(middleware.Logger(nil).Middleware)

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
| `/files/{path...}` | the rest of the path, including slashes |
| `/assets/*` | the rest of the path, readable as `c.Param("*")` |

Children are tried in the order static, regular expression, parameter,
catch-all, so a literal path always wins. The walk backtracks: with
`/a/{x}/c` and `/a/b/d` registered, `/a/b/c` still reaches the first route.

A parameter must span a whole segment. `name-{id}` is a registration error; use
a regular expression parameter instead.

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

A middleware without settings is a plain generic function. One with settings is
a value with a generic `Middleware` method. Go 1.27 infers the context type in
both positions, so you never write it:

```go
r.Use(middleware.Recover, middleware.RequestID, middleware.RealIP)
r.Use(middleware.Logger(log).Middleware)
r.Use(middleware.CORS("https://app.example").Middleware)
r.Use(middleware.Timeout(5 * time.Second).Middleware)
```

Standard `func(http.Handler) http.Handler` middleware works through an adapter:

```go
r.Use(router.WrapMiddleware[*Context](gziphandler.GzipHandler))
r.GET("/metrics", router.WrapHandler[*Context](promhttp.Handler()))
```

`middleware.Timeout` puts a deadline on the request context and does not
abandon a running handler, so your handler has to watch the context. Read
`RealIP` before you trust it: use it only behind a proxy that rewrites the
forwarding headers.

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
| **go-router** | 103 ns, 1 alloc | 98 ns, 1 alloc | 164 ns, 1 alloc |
| chi | 118 ns, 2 allocs | 210 ns, 4 allocs | 307 ns, 4 allocs |
| echo | 33 ns, 0 allocs | 40 ns, 0 allocs | 66 ns, 0 allocs |
| `http.ServeMux` | 99 ns, 0 allocs | 100 ns, 1 alloc | 254 ns, 3 allocs |

The one allocation is your context. Route parameters land in an array inside
`Base`, so matching itself allocates nothing for up to eight parameters. Echo
reaches zero by pooling its contexts; this router allocates yours fresh every
time, so a field a handler sets can never survive into the next request.

## What Go 1.27 buys

- **Generic methods** give `c.Bind[CreateUser]()`, `c.ParamAs[int]("id")` and
  `res.JSON[User]()` — the caller names the type, no out-parameter.
- **Generic methods** also give `r.MountRouter(prefix, sub)`, which accepts a
  router whose context type differs from the caller's.
- **Generalized function type inference** is what makes `r.Use(middleware.Recover)`
  and `r.Use(middleware.Logger(log).Middleware)` compile without a type argument.
- **`encoding/json/v2`** is the codec: stricter, and faster to decode.
- **stdlib `uuid`** backs `middleware.RequestID`.
- **`testing/synctest`** runs the timeout test on a fake clock.

## Limitations

- A parameter must span a whole segment. Use `{name:regexp}` for the rest.
- Register every route before the first request. The router compiles its trie
  once and refuses a later change.
- Contexts are not pooled.

## License

MIT. See [LICENSE](LICENSE).
