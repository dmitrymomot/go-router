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
	"syscall"

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
	r.Use(middleware.RequestID[*Context], middleware.Logger[*Context], middleware.Recover[*Context])

	// findUser is yours; the router never sees the database.
	r.GET("/users/{id}", func(c *Context) error {
		u, ok := findUser(c.DB, c.Param("id"))
		if !ok {
			return router.ErrNotFound.WithMessage("no user %s", c.Param("id"))
		}
		return c.JSON(http.StatusOK, u)
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := serve.Run(ctx, r, serve.Config{Addr: ":8080"}); err != nil {
		log.Fatal(err)
	}
}
```

## What it does

Each line links to the example that proves it.

- [Routes with parameters](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-package), including [a parameter inside a segment](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Router.GET-PartialSegment) such as `/reports/rep-{date}.csv`, and [a class such as `{id:uuid}`](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Router.GET-ParamClass) that turns a malformed id away with a 404.
- [Groups](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Router.Route) and [mounts](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Router.Mount), and [a mounted router with a context type of its own](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Router.MountHandler).
- [Routing on the host](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Router.Host), wildcards and host parameters included. The middleware of a host or of a scope with a prefix also runs for its 404 and 405 answers.
- [Links built from the route patterns](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Expand), path, query or [host](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-MustExpand), escaped and checked to route back.
- [Redirect routes](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Router.Redirect) that keep their values and the query, and [a canonical host](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Router.RedirectHost) such as www to the apex.
- [Route metadata](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Router.Meta) that nested scopes inherit and [middleware reads while the route answers](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-MetaAs), such as the permission a route needs.
- [Binding](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.Bind) from JSON, [a form](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.BindForm) with its checkboxes, the query, the path or the headers, with [validation that keeps its own status](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Validator), [field errors a form can show again](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-FieldErrorsOf), and [a body cap for one route](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.SetBodyLimit) above or below the router's.
- [Errors that carry their own status](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-HTTPError.WithMessage), and one handler that writes them: [JSON on an API host](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-JSONErrorHandler), or [an error page of your own](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-HTTPErrorOf).
- [Middleware that logs or measures the status the error handler wrote](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-HandleError).
- [Rendering](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.Render), [buffered or streamed](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.RenderStream).
- [Server-sent events](https://pkg.go.dev/github.com/dmitrymomot/go-router/sse#example-Serve) that send JSON, text or rendered HTML, in package `sse`.
- htmx 4, in package `htmx`: [a fragment or the whole page](https://pkg.go.dev/github.com/dmitrymomot/go-router/htmx#example-RenderPartial) from one handler, and [htmx answers](https://pkg.go.dev/github.com/dmitrymomot/go-router/htmx#example-NewResponse): retarget, reswap, trigger, redirect.
- [Cookies with safe defaults](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.NewCookie), and, in package `cookie`, [signed cookies](https://pkg.go.dev/github.com/dmitrymomot/go-router/cookie#example-Codec) with a codec your context holds, [key rotation](https://pkg.go.dev/github.com/dmitrymomot/go-router/cookie#example-NewCodec-Rotation), and [flash messages](https://pkg.go.dev/github.com/dmitrymomot/go-router/cookie#example-Codec.Flashes) that survive a redirect or show in an htmx partial.
- [Middlewares](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware): [CSRF](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-CSRF), [rate limit](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-RateLimit), [real IP](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-RealIPWithConfig), [key auth](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-KeyAuth), [CORS](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-CORSWithConfig), [a body limit](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-BodyLimit), [form parsing before the handler](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-ParseForm), [a timing floor against account enumeration](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-MinDuration), [idempotent submits](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-Idempotency), and the rest.
- [Fingerprinted static assets](https://pkg.go.dev/github.com/dmitrymomot/go-router/static#example-package) from an `embed.FS`.
- [A server that drains on Ctrl-C](https://pkg.go.dev/github.com/dmitrymomot/go-router/serve#example-Run), [leaves rotation before it drains](https://pkg.go.dev/github.com/dmitrymomot/go-router/serve#example-Config-DrainDelay), and [a private server that outlives the public one](https://pkg.go.dev/github.com/dmitrymomot/go-router/serve#example-RunAll).
- [Test helpers](https://pkg.go.dev/github.com/dmitrymomot/go-router/routertest#example-package) for a router, [a browser session that keeps its cookies](https://pkg.go.dev/github.com/dmitrymomot/go-router/routertest#example-Client), [one request for each route](https://pkg.go.dev/github.com/dmitrymomot/go-router/routertest#example-Requests), a bare handler, an SSE stream, [a signed cookie](https://pkg.go.dev/github.com/dmitrymomot/go-router/routertest#example-SignedCookie), [flash messages](https://pkg.go.dev/github.com/dmitrymomot/go-router/routertest#example-Flashes) and a golden file.
- [A stdlib handler](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-WrapHandler) still works, route parameters and all.

## Examples you can run

| Directory | Shows |
| --- | --- |
| [`_examples/restapi`](_examples/restapi) | a JSON API: a mounted sub-router, binding, validation, domain errors, a JSON error handler |
| [`_examples/chat`](_examples/chat) | a chat room: `html/template`, htmx and server-sent events |
| [`_examples/tenants`](_examples/tenants) | a multi-tenant app: signup on the apex, and a workspace with its own door on each subdomain |

## License

MIT. See [LICENSE](LICENSE).
