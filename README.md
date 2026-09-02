# go-router

An HTTP router for Go that hands your own struct to every handler.

Every handler takes a context you declare, so the database, the session and the signed-in user are fields rather than lookups in `r.Context()`. A handler returns an error, and one error handler turns it into an answer. Needs Go 1.27.

```bash
go get github.com/dmitrymomot/go-router
```

## A whole program

```go
package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"os/signal"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
	"github.com/dmitrymomot/go-router/serve"
)

// Context is yours. Put the database, the session and the user in it.
type Context struct {
	router.Base
	DB *sql.DB
}

func main() {
	db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}

	r := router.New(func(http.ResponseWriter, *http.Request) *Context {
		return &Context{DB: db}
	})
	r.Use(middleware.Recover[*Context], middleware.RequestID[*Context], middleware.Logger[*Context])

	// findUser is yours; the router never sees the database.
	r.GET("/users/{id}", func(c *Context) error {
		u, ok := findUser(c.DB, c.Param("id"))
		if !ok {
			return router.ErrNotFound.WithMessage("no user %s", c.Param("id"))
		}
		return c.JSON(http.StatusOK, u)
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := serve.Run(ctx, r, serve.Config{Addr: ":8080"}); err != nil {
		log.Fatal(err)
	}
}
```

## What it does

Each line links to the example that proves it.

- [Routes with parameters](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-package), including [a parameter inside a segment](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Router.GET-partialSegment) such as `/reports/rep-{date}.csv`.
- [Groups](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Router.Route) and [mounts](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Router.Mount), and [a mounted router with a context type of its own](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Router.MountRouter).
- [Routing on the host](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Router.Host), wildcards and host parameters included.
- [Binding with validation](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.Bind), from JSON, a form, the query, the path or the headers.
- [Errors that carry their own status](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-HTTPError.WithMessage), and one handler that writes them.
- [Rendering](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.Render), [buffered or streamed](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.RenderStream).
- [Server-sent events](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-ServeSSE) that send JSON, text or rendered HTML.
- [htmx answers](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.HX): retarget, reswap, trigger, redirect.
- [Fifteen middlewares](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware): [CSRF](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-CSRF), [rate limit](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-RateLimit), [real IP](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-RealIPWithConfig), [key auth](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-KeyAuth), [CORS](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-CORSWithConfig), and the rest.
- [Fingerprinted static assets](https://pkg.go.dev/github.com/dmitrymomot/go-router/static#example-package) from an `embed.FS`.
- [A server that drains on Ctrl-C](https://pkg.go.dev/github.com/dmitrymomot/go-router/serve#example-Run).
- [Test helpers](https://pkg.go.dev/github.com/dmitrymomot/go-router/routertest#example-package) for a router, a bare handler, an SSE stream and a golden file.
- [A stdlib handler](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-WrapHandler) still works, route parameters and all.

## Examples you can run

| Directory | Shows |
| --- | --- |
| [`_examples/restapi`](_examples/restapi) | a JSON API: a mounted sub-router, binding, validation, domain errors, a JSON error handler |
| [`_examples/chat`](_examples/chat) | a chat room: `html/template`, htmx and server-sent events |

## License

MIT. See [LICENSE](LICENSE).
