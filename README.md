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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := serve.Run(ctx, newRouter(db), serve.Config{Addr: ":8080"}); err != nil {
		log.Fatal(err)
	}
}

func newRouter(db *sql.DB) *router.Router[*Context] {
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
	return r
}
```

And its test, with package `routertest`, which sends a request through the whole router and checks the answer:

```go
func TestShowUser(t *testing.T) {
	r := newRouter(testDB(t)) // testDB is yours: a database that holds user 7, Ann.

	routertest.Get(r, "/users/7").Expect(t).
		Status(http.StatusOK).
		ContentType(router.MIMEApplicationJSON).
		JSONPath("name", "Ann")

	routertest.Get(r, "/users/8").Expect(t).
		Status(http.StatusNotFound).
		Contains("no user 8")
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
- [Binding](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.Bind) from JSON, [a form](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.BindForm) with its checkboxes, the query, the path or the headers, with [validation that keeps its own status](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Validator), [field errors a form can show again](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-FieldErrorsOf), and [a body cap for one route](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.SetBodyLimit) above or below the router's. The query and a form also fill [comma lists, defaults, nested structs and maps](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.BindQuery), and [a decoder you register](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Router.Decoder) adds a body type such as XML.
- [Errors that carry their own status](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-HTTPError.WithMessage), and one handler that writes them: [JSON on an API host](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-JSONErrorHandler), [RFC 9457 problem details](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-ProblemErrorHandler), or [an error page of your own](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-HTTPErrorOf).
- [Middleware that logs or measures the status the error handler wrote](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-HandleError).
- [Rendering](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.Render), [buffered or streamed](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.RenderStream).
- [A redirect that stays on the site](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.LocalRedirect), for a `next` parameter, and [a 304 for a client that already holds the page](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.CheckETag).
- [Server-sent events](https://pkg.go.dev/github.com/dmitrymomot/go-router/sse#example-Serve) that send JSON, text or rendered HTML, in package `sse`.
- htmx 4, in package `htmx`: [a fragment or the whole page](https://pkg.go.dev/github.com/dmitrymomot/go-router/htmx#example-RenderPartial) from one handler, and [htmx answers](https://pkg.go.dev/github.com/dmitrymomot/go-router/htmx#example-NewResponse): retarget, reswap, trigger, redirect.
- Translations, in package `i18n`, from catalogs your application ships and nothing built in: [a locale from the user, a cookie or Accept-Language](https://pkg.go.dev/github.com/dmitrymomot/go-router/i18n#example-MiddlewareWithConfig) that `i18n.T(c, ...)` reads in a handler or a component, [the locale in the path](https://pkg.go.dev/github.com/dmitrymomot/go-router/i18n#example-RedirectToLocale) with a 404 for any other, [plurals, and number, currency and date formats](https://pkg.go.dev/github.com/dmitrymomot/go-router/i18n#example-package), with CLDR rules in `i18n/cldr` that you wire per language.
- [Cookies with safe defaults](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.NewCookie), and, in package `cookie`, [signed cookies](https://pkg.go.dev/github.com/dmitrymomot/go-router/cookie#example-Codec) with a codec your context holds, [encrypted ones](https://pkg.go.dev/github.com/dmitrymomot/go-router/cookie#example-NewEncryptedCodec), [key rotation](https://pkg.go.dev/github.com/dmitrymomot/go-router/cookie#example-NewCodec-Rotation), and [flash messages](https://pkg.go.dev/github.com/dmitrymomot/go-router/cookie#example-Codec.Flashes) that survive a redirect or show in an htmx partial.
- [Middlewares](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware): [CSRF](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-CSRF), [rate limit](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-RateLimit), [real IP](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-RealIPWithConfig), [key auth](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-KeyAuth), [CORS](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-CORSWithConfig), [a body limit](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-BodyLimit), [form parsing before the handler](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-ParseForm), [a timing floor against account enumeration](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-MinDuration), [idempotent submits](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-Idempotency), [a concurrency cap](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-Throttle), [ETags for dynamic pages](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-ETag), [gzip and deflate](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-Gzip), [a token-less cross-origin guard](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-CrossOrigin), [a health probe](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-Heartbeat), [deprecation and sunset headers](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-Deprecation), [a body type allowlist](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-AllowContentType), [no-cache headers](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-NoCache), [trailing-slash stripping](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-StripSlashes), [a method override for HTML forms](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-MethodOverride), [load shedding under pressure](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-LoadShedWithConfig), [an address allowlist](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-IPFilterWithConfig), [a typed content security policy with a nonce for each request](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-CSPNonce), and the rest. [The client address](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-Base.ClientIP) comes from `c.ClientIP()`, [behind Cloudflare, Fastly, CloudFront or another edge](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware#example-TrustCloudflare) too, and [pprof and expvar](https://pkg.go.dev/github.com/dmitrymomot/go-router/middleware/profiler#example-Handler) mount from package `middleware/profiler`.
- [Liveness and readiness probes](https://pkg.go.dev/github.com/dmitrymomot/go-router/health#example-package), in package `health`, whose readiness turns 503 as soon as the server begins to stop, on the public router or [a private server](https://pkg.go.dev/github.com/dmitrymomot/go-router/health#example-Handler).
- [The tenant of a request](https://pkg.go.dev/github.com/dmitrymomot/go-router/tenant#example-package), in package `tenant`, from a host such as `{tenant}.example.com`, a path, a header, a cookie or [your own context](https://pkg.go.dev/github.com/dmitrymomot/go-router/tenant#example-MiddlewareWithConfig), as a type of your own that any context of the request reads.
- [The user of a request](https://pkg.go.dev/github.com/dmitrymomot/go-router/user#example-package), in package `user`, from the session, a signed cookie, a Bearer token or [your own context](https://pkg.go.dev/github.com/dmitrymomot/go-router/user#example-MiddlewareWithConfig), as a type of your own that any context of the request reads, with a gate that answers 401 or sends a browser to the login page.
- [Sessions](https://pkg.go.dev/github.com/dmitrymomot/go-router/session#example-package), in package `session`, that hold a value of your own type in [a store on the server](https://pkg.go.dev/github.com/dmitrymomot/go-router/session#Store), which can revoke them, or [in an encrypted cookie](https://pkg.go.dev/github.com/dmitrymomot/go-router/session#example-NewCookieStore), with a new token at login, an idle timeout and a lifetime.
- [Page and cursor parameters](https://pkg.go.dev/github.com/dmitrymomot/go-router/pagination#example-Page) that `Bind` fills and checks, in package `pagination`, with a limit and an offset ready for SQL.
- [The browser, the system, the device and the bot](https://pkg.go.dev/github.com/dmitrymomot/go-router/useragent#example-Of) behind a request, in package `useragent`, with Client Hints that [it asks for](https://pkg.go.dev/github.com/dmitrymomot/go-router/useragent#example-AcceptCH).
- [Fingerprinted static assets](https://pkg.go.dev/github.com/dmitrymomot/go-router/static#example-package) from an `embed.FS`, with the precompressed `.br` or `.gz` sibling of a file sent to a client that accepts it.
- [A server that drains on Ctrl-C](https://pkg.go.dev/github.com/dmitrymomot/go-router/serve#example-Run), [leaves rotation before it drains](https://pkg.go.dev/github.com/dmitrymomot/go-router/serve#example-Config-DrainDelay), [a private server that outlives the public one](https://pkg.go.dev/github.com/dmitrymomot/go-router/serve#example-RunAll), [workers that stop after the drain](https://pkg.go.dev/github.com/dmitrymomot/go-router/serve#example-Config-Background), and [the JA4 of a TLS client](https://pkg.go.dev/github.com/dmitrymomot/go-router/serve#example-JA4), captured by the server or reported by a CDN.
- [Test helpers](https://pkg.go.dev/github.com/dmitrymomot/go-router/routertest#example-package) for a router, [a browser session that keeps its cookies](https://pkg.go.dev/github.com/dmitrymomot/go-router/routertest#example-Client), [one request for each route](https://pkg.go.dev/github.com/dmitrymomot/go-router/routertest#example-Requests), a bare handler, an SSE stream, [a signed cookie](https://pkg.go.dev/github.com/dmitrymomot/go-router/routertest#example-SignedCookie), [flash messages](https://pkg.go.dev/github.com/dmitrymomot/go-router/routertest#example-Flashes), [a JSON body compared by value](https://pkg.go.dev/github.com/dmitrymomot/go-router/routertest#example-Expect.JSON), [the first events of a live stream](https://pkg.go.dev/github.com/dmitrymomot/go-router/routertest#example-ReadEvents) and a golden file.
- [A stdlib handler](https://pkg.go.dev/github.com/dmitrymomot/go-router#example-WrapHandler) still works, route parameters and all.

## Coming from chi or echo

| chi or echo | go-router |
| --- | --- |
| `echo.Context`, or `r.Context()` values in chi | your struct embedding `router.Base`, with the database and the user as fields |
| `func(w, r)`, or `func(c echo.Context) error` | `func(c *Context) error` |
| `chi.URLParam(r, "id")`, `c.Param("id")` | `c.Param("id")`, or `c.ParamAs[int]("id")` |
| `c.QueryParam("q")` | `c.Query("q")`, `c.QueryAs[int]("page")` |
| `c.Bind(&v)` | `v, err := c.Bind[T]()`, or `BindJSON`, `BindForm`, `BindQuery`, `BindPath`, `BindHeader` |
| `c.RealIP()` | `c.ClientIP()`, behind `middleware.RealIP` |
| `r.Group(fn)`, `r.Route("/api", fn)`, `e.Group("/api")` | `r.Group(fn)`, `r.Route("/api", fn)` |
| `r.With(mw)` | `r.With(mw)` |
| `r.Mount("/admin", sub)` | `r.Mount("/admin", sub)`, or `r.MountHandler("/admin", h)` for any `http.Handler` |
| `e.Host("api.example.com")` | `r.Host("api.example.com", fn)`, host parameters included |
| `e.Pre(mw)` | `r.Pre(mw)` |
| `e.HTTPErrorHandler = fn` | `r.ErrorHandler(fn)`, per scope if you like |
| `echo.NewHTTPError(404, "msg")` | `router.ErrNotFound.WithMessage("msg")` |
| a reverse lookup by route name | `router.Expand("/users/{id}", "id", id)` |
| `echo.WrapHandler(h)` | `router.WrapHandler[*Context](h)` |
| `middleware.Static`, `http.FileServer` | `static.Mount(r, assets)` |
| `middleware.Compress`, echo `middleware.Gzip` | `middleware.Gzip` |
| `middleware.Recoverer`, echo `middleware.Recover` | `middleware.Recover` |
| `middleware.Throttle` | `middleware.Throttle` |
| `middleware.Heartbeat`, `middleware.StripSlashes`, `middleware.NoCache`, `middleware.AllowContentType` | the same names in package `middleware` |
| `middleware.Profiler` | `r.MountHandler("/debug", profiler.Handler())`, behind auth |
| echo `middleware.RateLimiter` | `middleware.RateLimit` |
| `http.ListenAndServe`, `e.Start` | `serve.Run(ctx, r, serve.Config{Addr: ":8080"})` |

## Examples you can run

| Directory | Shows |
| --- | --- |
| [`_examples/restapi`](_examples/restapi) | a JSON API: a mounted sub-router, binding, validation, domain errors, a JSON error handler |
| [`_examples/chat`](_examples/chat) | a chat room: `html/template`, htmx and server-sent events |
| [`_examples/tenants`](_examples/tenants) | a multi-tenant app: signup on the apex, and a workspace with its own door on each subdomain |

## License

MIT. See [LICENSE](LICENSE).
