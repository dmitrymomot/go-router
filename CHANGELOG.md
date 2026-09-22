# Changelog

All notable changes to this module are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the module follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Before v1, a minor release may break the API; each break is listed below with the way to migrate.

## [0.2.0] - 2026-09-22

v0.2.0 breaks the API in several places. Read [Upgrading from v0.1](#upgrading-from-v01) before you bump the version: most breaks fail to compile, but the first two change behavior silently.

### Breaking changes

Ordered by risk, the silent ones first.

1. **A bare-word constraint names a parameter class.** A constraint made only of letters, digits and `_` used to compile as a regular expression that matched that word alone, so `{id:uuid}` matched the path `/x/uuid` and nothing else. It now names a class: `{id:int}`, `{t:slug}` and `{id:uuid}` admit digits, a slug or a canonical UUID. Any other bare word, such as `{v:v1}` or `{id:123}`, panics at the call that registers it. The rule also covers the patterns of `Host`, `Hosts`, `HostHandler` and `HostRouter`, and the prefixes of `Route`, `Mount` and `MountHandler`. `ValidatePattern` now reports a bare word no class could have, one that starts with a digit or `_`. `Mount` panics when a parent and a mounted router register the same host pattern text and one of its classes resolves to a different declaration in each.
2. **RealIP keeps the scheme of a trusted proxy.** `RealIP` and `RealIPWithConfig` keep the `X-Forwarded-Proto` of a trusted peer, a loopback, private or link-local one under bare `RealIP` included, where v0.1 deleted it unless `Headers` named it. `Base.Scheme`, `SchemeOf`, the `Secure` flag of the CSRF, flash and `NewCookie` cookies, and HSTS now follow the proxy. The kept value is reduced to one lowercase `http` or `https`: the entry the trusted peer wrote last, or the first entry under `Leftmost`; any other value is deleted, where v0.1 passed a named header through verbatim (`HTTPS, http`). Under `Leftmost`, which vouches for every peer, the header is kept from any peer, an untrusted one included. `RealIPWithConfig` panics when `Headers` names `X-Forwarded-Proto`, in any letter case. `RealIPConfig` gains `DropProto`, so a positional `RealIPConfig{...}` literal no longer compiles.
3. **Every error handler returns an error, and the router guards and logs around it.** `ErrorHandlerFunc[C]` is now `func(c C, err error) error`, and `DefaultErrorHandler` returns `error`. For every handler the router skips a committed response and a `context.Canceled` error, sends a bare 500 when the handler fails before it writes anything (it used to leave an empty 200), and logs the failure with the status that went out: 5xx at Error, and at Warn a 4xx other than a bare `HTTPError` with no cause. A custom handler used to be called for a committed response and was never logged; an app that logs inside its handler now logs twice. `DefaultErrorHandler` and `ErrorHandler(exposeCause)` become plain writers: called directly, they neither log nor skip a committed response. `StatusOf`, and so `ResolveStatus`, report 499 instead of 500 for `context.Canceled`, so a `>= 500` check no longer counts a client that went away. `middleware.Logger` answers the error itself through `HandleError` before it logs, so it logs the status and size the handler wrote; the answer is committed there, so Logger goes outside `Timeout` and any middleware that replaces an error after `next`. Parameter 404s and Bind 400s carry a cause, so each one now logs a Warn line.
4. **htmx 2 is no longer supported.** `HTMXWantsPartial`, `HTMXPartial`, `Base.WantsPartial`, `RenderPartial` and `middleware.HTMXRedirect` read `HX-Request` and `HX-Request-Type` only, and vary on those two: the `Vary` list changes from `Hx-Request, Hx-Boosted, Hx-History-Restore-Request` to `Hx-Request, Hx-Request-Type`. An htmx 2 boosted link or history restore, which sends no `HX-Request-Type`, now gets the fragment and has its redirect turned into `HX-Redirect`. `HTMXRedirect` keeps the 3xx of an htmx 4 full request (a boost, a history restore, `hx-select`, a body target) and turns a partial one even when it is boosted. `HTMXRequest.Target` now holds htmx 4's `tag#id`, such as `ul#user-list`, not a bare id. `HTMXRequest` drops `Prompt`, `Trigger` and `TriggerName` and gains `RequestType` and `Source`, so a positional `HTMXRequest{...}` literal no longer compiles. `routertest.HTMX()` also sends `Hx-Request-Type: partial`. The removed names are listed under [Removed](#removed).
5. **One cookie codec for the whole router.** `Router.CookieCodec(cc)` sets the codec that signed cookies and flashes use, and the methods drop their codec argument: `AddFlash(cc, f)` becomes `AddFlash(f)`, `Flashes(cc)` becomes `Flashes()`, `SetSignedCookie(cc, ck)` becomes `SetSignedCookie(ck) error`, and `SignedCookie(cc, name) ([]byte, error)` becomes `SignedCookie(name) (string, error)`. Without a codec, `AddFlash`, `SetSignedCookie` and `SignedCookie` report `ErrNoCookieCodec`, which answers 500, and `Flashes` returns nil even when the request carries a valid `_flash` cookie; that includes every `NewBase` and `routertest.NewContext` context. `Mount` panics on a sub-router that set a codec (`the mounted router carries a cookie codec`). `NewCookieCodec(key)` becomes `NewCookieCodec(key, previous ...[]byte)`: every call still compiles, but a function value typed `func([]byte) *CookieCodec` does not. On the wire, a flash added and read back in the same request, when the request carried no `_flash` cookie, no longer sends a `Max-Age=0` `_flash` line; that line could delete a flash another tab's redirect had set.
6. **`Base.Cookie` returns the value.** `Cookie(name) (*http.Cookie, error)` becomes `Cookie(name) string`, which is `""` when the cookie is missing or empty. `Cookie` and `SetCookie` move to `cookie.go`, which changes nothing for callers.
7. **BodyLimit replaces the router's body cap.** `middleware.BodyLimit` now sets the cap of the Bind methods and form readers through `SetBodyLimit`, so it raises `Router.MaxBodyBytes` as well as lowering it; before, the smaller of the two won. `BodyLimit(0)` still means `DefaultMaxBodyBytes`, which now also replaces `MaxBodyBytes` on those routes. Behind `Decompress`, the cap now bounds the expanded body that Bind reads, where `MaxBodyBytes` applied before. `BodyLimit` no longer copies the request or calls `SetRequest`: it wraps `Body` in place, so a middleware in front that kept the original `*http.Request` sees the capped body.
8. **A path parameter that does not parse answers 404.** `ParamAs` reports `ErrNotFound` (404 `Not Found`, with the parse error only in the cause) where it reported 400, and `ErrInternalServerError` for a name the route does not have. `BindPath` reports `ErrNotFound` with no Details where it reported 400 `invalid request` with Details; `FieldErrorsOf` still finds the fields in the cause. In `_examples/restapi`, a PUT or DELETE with a bad id now answers 404 before the auth middleware runs, where it answered 401.
9. **Bind reports its errors differently.**
   - A Validator error that is or wraps an `*HTTPError` or a `StatusCoder` keeps its status, message and Details; it always became a stock 422 before. Bind may return that wrapper unchanged, so a Bind error is no longer always a bare `*HTTPError` by type.
   - BindJSON with a member of the wrong type, a rejected value, or an unknown member (under `RejectUnknownMembers`) answers 400 `invalid request` with one `FieldError` at the JSON path, such as `events[3].amount`, and a fixed message: `has the wrong JSON type`, `is not a valid value` or `is not a known field`. The message used to carry the json/v2 text. A syntax error answers exactly `malformed JSON body`, and a root of the wrong type answers `the request body has the wrong JSON type`. The decoder error stays in `Err`.
   - A pointer that fails to parse stays nil, where it pointed at a zero value: in `*T` fields of `BindForm`, `BindQuery`, `BindPath` and `BindHeader`, and in `ParseValue[*T]`, `QueryAs[*T]`, `ParamAs[*T]` and `FormAs[*T]` on error. Bind returns the partly bound value along with its error.
   - Booleans accept `on` and `off` in any letter case, as a checkbox sends them, everywhere a scalar is parsed: every Bind tag, `FormAs`, `QueryAs`, `QueryAllAs`, `ParamAs`, their `Default` and `OK` forms, and `ParseValue`. JSON is untouched. A request that sent `on` used to get a 400.
   - A malformed query string no longer fails the form readers with 400 `malformed form body`. When the router parses the form itself, it leaves `http.Request.Form` nil afterwards, and net/http rebuilds it on the next `ParseForm` or `FormValue`.
10. **Route metadata is a list, and scopes inherit it.** `Route.Meta` changes from `any` to `[]any`, so `Route` is no longer comparable with `==`. `Router.Meta(v any)` becomes `Meta(v ...any)`; one-value calls still compile, but `Meta()` and `Meta(nil)` panic, where `Meta(nil)` registered nothing or cleared the value of a scope that `Meta` returned. A `Route`, `Group`, `With` or `Host` opened inside a Meta scope, and a router mounted into one, now inherit its values, and `Meta` on a Meta scope adds a value instead of replacing it. `Meta` after serving panics with `cannot change the route metadata after the router started serving`.
11. **The `Context` interface gains three methods, and `SetRequest` refuses a context derived from the Base.** `Context` gains `SetContext(ctx)`, `SetBodyLimit(n)` and `RouteMeta() []any`, in the order `SetRequest, SetContext, SetBodyLimit, Response, …, RoutePattern, RouteMeta, RouteHost`. A context type that embeds `Base` gets them for free, but one that declares its own member of that name with another signature no longer satisfies `Context`. `SetRequest` panics when the request's context is the Base or derives from it, as in `c.SetRequest(c.Request().WithContext(context.WithValue(c, k, v)))`; that code used to end in a fatal stack overflow on the first `Value`, `Done`, `Err` or `Deadline` lookup that missed.
12. **routertest: the Assert methods are gone.** `(*Response).AssertStatus`, `AssertBody` and `AssertHeader` are removed in favor of `Expect`. `Expect(t).Body` and `Header` report a miss with `Errorf` and let the test go on, where `AssertBody` and `AssertHeader` stopped it with `Fatalf`. Without `WithRequest` or `WithTarget`, `NewContext` builds its request on `tb.Context()` instead of `context.Background()`, so a handler that outlives the test sees a canceled context, and a fake `testing.TB` whose embedded TB is nil must implement `Context()`. `Serve`, `Do` and `Get` now fill `Response.Request`, which was nil.
13. **serve: two new Config fields and an earlier TLS check.** `Config` gains `DrainDelay` and `OnDrain`, so a positional `Config{...}` literal no longer compiles. `Run` reports a TLS config with no certificate (no `Certificates`, `GetCertificate` or `GetConfigForClient`) before it listens, after `OnServer`, so `OnListen` no longer runs in that case, and the error changes from net/http's `open : no such file or directory` to `serve: the TLS config has no certificate; ...`. Two messages no longer name Run: `serve: Run needs a handler` becomes `serve: the server needs a handler`, and `serve: Run needs Config.Addr or Config.Listener` becomes `serve: the server needs Config.Addr or Config.Listener`.

### Added

Routing and URLs:

- Parameter classes: the built-in `{id:int}`, `{t:slug}` and `{id:uuid}` (the canonical 8-4-4-4-12 form in either case), and `Router.ParamClass(name, match)` for classes of your own. A value outside the class does not match the route.
- `Expand` and `MustExpand` fill a route or host pattern with escaped values, and refuse a value that would not route back to the same parameters or a path that would start with `//`.
- `Router.Redirect(pattern, target, status)`, a GET and HEAD route that redirects with the route's values and keeps the request query after the target's own pairs, so `Query().Get` reads the target's value.
- `Router.RedirectHost(pattern, target, status)` sends every request for a host to another, keeping the scheme, port, path and query, and owns that host: a route there panics. A request that already names the target gets 404 instead of a loop, so `RedirectHost("*", apex, ...)` also answers load-balancer probes on unknown hosts with a redirect.
- `Base.RouteMeta()` and `MetaAs[T](c)` read route metadata while the route answers. `unsafe.Sizeof(Base{})` shrinks from 304 to 296 bytes.

Requests and binding:

- `Base.SetContext(ctx)` replaces the request context and keeps the cached query and host, in place of `SetRequest(Request().WithContext(ctx))`. `middleware.Timeout` uses it.
- `Base.SetBodyLimit(n)`, the body cap of one request, above or below `MaxBodyBytes`.
- `Base.FormRequired(name)`, a form field that must not be empty.
- `FieldErrorsOf(err)` reads the fields an error names, through `errors.Join` and `%w`.

Errors:

- `HandleError(c, err)` answers an error now, so a middleware reads the final status and size.
- `HTTPErrorOf(err)`, the `*HTTPError` the client is answered with.
- `JSONErrorHandler[C]` and `ErrorBody{Status, Message, Details}`, a JSON answer that keeps the cause out of the body.

htmx 4:

- `Base.WantsPartial()` and `Base.RenderPartial(status, partial, page)` pick a fragment or the whole page by `HX-Request-Type`.
- `HTMXRequest.RequestType`, `Source`, `TargetID()` and `SourceID()`, and the headers `HeaderHXRequestType` and `HeaderHXSource`.

Cookies and flashes:

- `Router.CookieCodec(cc)`, one codec for signed cookies and flashes, and `ErrNoCookieCodec`.
- `NewCookieCodec(key, previous...)` signs with `key` and also verifies with each previous key, for key rotation. A `CookieCodec` may now be copied; a copy shares the keys. The wire format is unchanged, so v0.1 cookies still verify with the same key.
- `Base.NewCookie(name, value, maxAge)`, a cookie with safe defaults whose `Secure` follows `Scheme()`, and `Base.ClearCookie(name)`.
- `CookieCodecOf(h)` and `SetCookieCodecForTest(b, cc)` let test tooling reach the codec of a router.

Responses:

- `Response.Capture(limit)` records an answer as it goes out and returns `Recorded{Header, Body, Status, Truncated}`.
- `HeaderIdempotencyKey`.

middleware:

- `ClientAddr[C](c)`, the peer address as a `netip.Addr`, and `RealIPConfig.DropProto`.
- `ParseForm` and `ParseFormWithConfig`, which answer 400 or 413 for a form that does not parse before the handler runs.
- `MinDuration(d)` and `MinDurationWithConfig(MinDurationConfig{Skip, Duration})`, a response-time floor against account enumeration. They hold every answer of the covered routes until `d` has passed since the middleware ran: error answers, a panic's 500, an empty 200 and a streamed first `Flush` included. The hold ends early when the request context found at entry ends, and an inner `Timeout` does not shorten it. They panic when `d <= 0`.
- `Idempotency(store)` and `IdempotencyWithConfig(IdempotencyConfig{Skip, Store, Scope, Fingerprint, Sources, MaxBody, Wait, Required})` run an unsafe request once per `Idempotency-Key` and replay its answer. A key reused for a different request (another method, path or form body) answers 422 `ErrIdempotencyKeyReused`, whether the first request is done or still running; a repeat of one still running waits up to `Wait`, then answers 409 `ErrIdempotencyInProgress`. Also `IdempotencyStore` (`Claim`, `Complete`, `Release`), `IdempotencyEntry`, `NewIdempotencyMemoryStore` and `NewIdempotencyMemoryStoreWithConfig(IdempotencyMemoryStoreConfig{ExpiresIn, MaxEntries})`, `IdempotencyFormFingerprint`, the errors `ErrIdempotencyKeyRequired` (400), `ErrIdempotencyKeyReused` (422), `ErrIdempotencyInProgress` (409) and `ErrIdempotencyTooLarge` (409), and the constants `DefaultIdempotencyFormField`, `DefaultIdempotencyMaxBody`, `DefaultIdempotencyWait`, `DefaultIdempotencyExpiry`, `DefaultIdempotencyMaxEntries` and `MaxIdempotencyKeyLength`. The default fingerprint covers the method, the path and the form body, but not the query, the host, a JSON body, or file contents beyond field, name, size and type; a JSON API sets `Fingerprint` (see `ExampleIdempotencyFormFingerprint`).
- The package doc has an Order section with one canonical middleware order, and every middleware doc links to it.

routertest:

- `Client` (`NewClient`, `Do`, `Get`, `Follow`, `Cookie`, `SetCookie`) keeps the cookies of every answer as a browser does, and sends a Secure cookie only over https.
- `Response.Expect(t)` with the chainable checks `Status`, `Body`, `Contains`, `NotContains`, `Header`, `ContentType`, `Redirect` and `FieldErrors`, which name the request on a miss.
- `Response.ErrorBody()` decodes a `JSONErrorHandler` answer.
- `Recorded(rec)` wraps an `httptest.ResponseRecorder` as a `Response`. A `Response` from `Serve` carries the request it answered, so `Location` resolves a relative redirect against that request.
- The request options `Context(ctx)` and `RemoteAddr(addr)`, and the context option `WithTarget(method, target, opts...)`.
- `Requests(routes, fill)` yields one request per route with every parameter filled in; built-in classes get valid values.
- `WithCookieCodec(cc)`, `SignedCookie(res, name)`, `Flashes(res)` and `FlashCookie(cc, flashes...)`.

serve:

- `Config.DrainDelay` and `Config.OnDrain`. When ctx ends, Run calls `OnDrain`, keeps answering for `DrainDelay` with keep-alives on, then drains within `ShutdownTimeout`. A negative `DrainDelay` is an error. A server whose serving fails closes at once, with no `OnDrain` and no delay.
- `Server{Handler, Config, Options}` and `RunAll(ctx, servers...)`. RunAll checks every server and opens every listener before any serves. One server stopping stops all of them, in the order given, and each error is prefixed `serve: server N: ` and unwraps to the cause. The package doc now reads "runs HTTP servers".

### Fixed

- `SetRequest` with a request context derived from the Base panics at the call instead of crashing the process with a stack overflow that no recover catches.
- Behind a proxy that ends TLS, `RealIP` no longer drops the scheme, so the CSRF cookie keeps `Secure` and HSTS goes out.
- `middleware.Logger` logs the status and size the error handler wrote. A plain error that the handler mapped to 423 used to be logged as a 500 with 0 bytes.
- A custom error handler no longer writes over a committed response, and its failures are logged.
- A handler that reads an oversized body itself and returns the `*http.MaxBytesError` answers 413 instead of 500, whatever the middleware order.
- A form POST whose query string is malformed binds its body instead of answering 400. For a multipart form, the files spilled to disk in that case are removed with the request again.
- A 413 behind a wrapped writer, such as Gzip, closes the connection again, and a body that BodyLimit cuts off closes the connection too.
- One upload route can raise the body cap above `Router.MaxBodyBytes` with `BodyLimit`.
- A Validator error keeps its own status instead of a stock 422, so a handler no longer validates twice to answer 409.
- The JSON decoder text, which names Go types and changes between runs, no longer reaches the client.
- Examples: `_examples/restapi` no longer sends `err.Error()` to the client, so a 500 does not leak its cause nor a `StatusCoder` its text. Its middleware follows the canonical order, so a 429 from `RateLimit` carries CORS headers and a panic gets the request line of `Logger`. The README and a comment there no longer claim that `Mount` refuses a sub-router with an error handler; it refuses `MaxBodyBytes` and a cookie codec. `_examples/chat` closes the room from `OnDrain`, where a goroutine on `ctx.Done` raced `Shutdown`.

### Removed

- htmx 2 only: `HeaderHXPrompt`, `HeaderHXTriggerName`, `HeaderHXTriggerAfterSwap`, `HeaderHXTriggerAfterSettle`, the `HTMXRequest` fields `Prompt`, `Trigger` and `TriggerName`, the `HXResponse` methods `TriggerAfterSwap`, `TriggerAfterSettle`, `TriggerEventsAfterSwap` and `TriggerEventsAfterSettle`, and `HXLocation.Handler`. `HeaderHXTrigger` stays as a response header.
- routertest: `(*Response).AssertStatus`, `AssertBody` and `AssertHeader`.

### Known limits

- With the json v1 legacy options (`jsonv1.DefaultOptionsV1()` through `JSONOptions`), a mistyped member gives a `*jsonv1.UnmarshalTypeError`, so BindJSON answers `malformed JSON body` with no field.
- A JSON member whose key is the empty string is reported as `the request body has the wrong JSON type`, with no field.
- `Expand` cannot see a class that a router declares with `ParamClass`, so it admits any value for one. It checks the built-in classes and regular expressions.
- `IdempotencyStore.Complete` and `Release` take only the key, so a run that outlives `ExpiresIn` can complete or release a later claim of the same key.
- Idempotency records the header after every `Response.Before` callback ran, those of the middleware in front included. A cookie that an outer callback sets only on the first request is therefore replayed, and a handler cookie with the name of one that the middleware in front sets for the repeat is dropped on replay.
- The Idempotency 409 sends no `Retry-After`, and a memory store full of running keys answers 500 rather than 503.
- The longest stop of `RunAll` is the sum of `DrainDelay + ShutdownTimeout` over all servers, so the orchestrator's grace period has to cover it. The `serve: server N: ` prefix covers only the first line of a joined error.

### Notes on the history

These notes correct commit messages that a history rewrite would otherwise have to fix. They matter for `git bisect` and for reading `git log`.

- Commits 1cfd1be through a04c954 fail `go test .`: `TestExpandCacheStaysBounded` fills the shared Expand cache before `TestExpandStaysAtTwoAllocations` runs. 80db408 fixes it, so `git bisect skip` that range.
- Commits 58b020b through bab365f pass build and vet but not `just check`. 2d22f7e (the `IdempotencyEntry` field order for betteralign) and f19886e (gofumpt on the test stubs) make the tree green again; neither changes behavior.
- These commits break the API or the wire without a `!` in their subject: 4e2613c (the variadic `NewCookieCodec`) and 1cc7655 (no `Max-Age=0` flash line on a request without the cookie). Both are listed above.
- The body of 706f1d3 leaves out that `Leftmost` keeps the scheme of an untrusted peer, and the body of b91b29e leaves out that Bind may return a wrapper around the Validator's `HTTPError`. Both are listed above.
- 80a6a11 is typed `docs(router)` but also adds the test `TestBindFormReturnsWhatDecoded`. af106fa both names the JSON member that does not fit and keeps the decoder text out of the message. The body of 3681184 says `QueryAllAs[*T]` now returns nil on error, but it already did.
- The body of e74cd0e says a panic had "no log line of its own" under the old restapi order. The router's error pipeline still logged it; what was missing was the request line of `Logger`.
- 58c3724 adds `routertest.Recorded` and also makes a served `Response` keep its request.

### Upgrading from v0.1

Each break below has a before and after, and a grep that finds the code to look at. Run the greps from the root of your module; most of them also match code that needs no change, so read each hit.

#### Parameter classes

Keep `{id:int}`, `{t:slug}` and `{id:uuid}` where you meant the class. A literal becomes a static segment or a non-capturing group, and a class of your own is declared on the root before the first pattern that uses it.

```go
// Before: matched only the literal path /api/v1/users.
r.GET("/api/{v:v1}/users", listUsers)
r.GET("/r/{code:shortid}", resolve)

// After
r.GET("/api/{v:(?:v1)}/users", listUsers)
r.ParamClass("shortid", func(v string) bool { return len(v) == 8 })
r.GET("/r/{code:shortid}", resolve)
```

```sh
grep -rnE '\{[A-Za-z_][A-Za-z0-9_]*:[A-Za-z0-9_]+\}' --include='*.go' .
```

For a mounted router and its parent that both register one host pattern, declare the class once, on the parent.

#### RealIP and X-Forwarded-Proto

Remove `X-Forwarded-Proto` from `Headers`; a trusted peer's scheme is kept without it. To take the scheme from the connection alone, as v0.1 did, set `DropProto`. Do the same under `Leftmost` wherever the scheme must not come from the chain.

```go
// Before
middleware.RealIPWithConfig[C](middleware.RealIPConfig{
	Headers: []string{router.HeaderXForwardedFor, router.HeaderXForwardedProto},
})

// After: the scheme of a trusted proxy
middleware.RealIPWithConfig[C](middleware.RealIPConfig{
	Headers: []string{router.HeaderXForwardedFor},
})

// After: the v0.1 behavior
middleware.RealIPWithConfig[C](middleware.RealIPConfig{
	Headers:   []string{router.HeaderXForwardedFor},
	DropProto: true,
})
```

```sh
grep -rnE 'HeaderXForwardedProto|X-Forwarded-Proto|RealIPConfig\{' --include='*.go' .
```

Behind a TLS proxy, code that read the raw header as a fallback can use `c.Scheme() == "https"` alone. Code that parsed the raw list now reads one value.

#### Error handlers

Return the write, and delete the `//nolint:errcheck` comments. Delete any logging inside the handler, because the router now logs the failure; a handler that only observed failures moves to `Router.Observe`.

```go
// Before
r.ErrorHandler(func(c *Ctx, err error) {
	c.Logger().Error("request failed", "err", err)
	c.JSON(router.StatusOf(err), body(err)) //nolint:errcheck
})

// After
r.ErrorHandler(func(c *Ctx, err error) error {
	return c.JSON(router.StatusOf(err), body(err))
})
```

A handler that falls back to the default writes `return router.DefaultErrorHandler(c, err)`. Code that called `DefaultErrorHandler` or `ErrorHandler(exposeCause)` directly and relied on the log or the committed guard calls `router.HandleError(c, err)`. Match 499 where a `>= 500` check has to keep counting client disconnects. Put `middleware.Logger` outside `Timeout` and any middleware that replaces an error on the way out; see Order in the middleware package doc. To silence the Warn lines, pass `Router.Logger` a `slog.Logger` whose handler filters by level.

```sh
grep -rnE '\.ErrorHandler\(|DefaultErrorHandler|StatusOf\(|ResolveStatus\(' --include='*.go' .
```

#### htmx 4

Move the page to htmx 4 (`htmx.org@4.x`), which sends `HX-Request-Type: full` for a boost and a history restore; to stay on htmx 2, stay on go-router v0.1. Then replace the removed names:

```go
// Before
_, id, _ := strings.Cut(c.HTMX().Target, "#") // or c.HTMX().Target == "user-list"
who := c.HTMX().Trigger                         // or c.Request().Header.Get(router.HeaderHXTrigger)
name := c.HTMX().TriggerName
answer := c.HTMX().Prompt
c.HX().TriggerAfterSwap("saved")
c.HX().LocationWith(router.HXLocation{Path: "/next", Handler: "onDone"})

// After
id := c.HTMX().TargetID()
who := c.HTMX().SourceID()
name := c.FormValue("name")                     // send it as a value: name/value on the element, or hx-vals
answer := c.Request().Header.Get("Hx-Prompt")   // with the htmx 4 prompt extension; otherwise a form field
c.HX().Trigger("saved")                         // htmx 4 fires HX-Trigger after the swap
c.HX().LocationWith(router.HXLocation{Path: "/next"}) // handle the swap with hx-on::after:swap
```

`TriggerEventsAfterSwap` and `TriggerEventsAfterSettle` become `TriggerEvents`. Write `HTMXRequest{...}` literals with field names. Tests and cache configs that pin the old `Vary` list expect `Hx-Request, Hx-Request-Type`. Where a client-side navigation is still wanted for a full request, answer `c.HX().Redirect(url)`. A test that needs a full request adds `routertest.Header(router.HeaderHXRequestType, "full")` to `routertest.HTMX()`.

```sh
grep -rnE 'HeaderHXPrompt|HeaderHXTriggerName|HeaderHXTriggerAfter|HTMX\(\)\.(Target|Trigger|TriggerName|Prompt)\b|\.TriggerName|\.Prompt\b|TriggerAfterSwap|TriggerAfterSettle|TriggerEventsAfter|Handler:|HeaderHXBoosted|HeaderHXHistoryRestoreRequest|Hx-Boosted|Hx-History-Restore-Request|HTMXRequest\{' --include='*.go' .
```

#### The router cookie codec

Set the codec once where the router is built, then drop the codec arguments. `SetSignedCookie` now returns an error.

```go
// Before
cc := router.NewCookieCodec(key)
c.SetSignedCookie(cc, &http.Cookie{Name: "sid", Value: id})
raw, err := c.SignedCookie(cc, "sid")
id := string(raw)
if err := c.AddFlash(cc, router.Flash{Kind: "success", Message: "saved"}); err != nil {
	return err
}
flashes := c.Flashes(cc)

// After
r.CookieCodec(router.NewCookieCodec(key))

if err := c.SetSignedCookie(&http.Cookie{Name: "sid", Value: id}); err != nil {
	return err
}
id, err := c.SignedCookie("sid")
if err := c.AddFlash(router.Flash{Kind: "success", Message: "saved"}); err != nil {
	return err
}
flashes := c.Flashes()
```

A handler that treats every `SignedCookie` error as signed out checks `errors.Is(err, router.ErrNoCookieCodec)` first. In tests, pass `routertest.WithCookieCodec(cc)` to `NewContext`, or call `router.SetCookieCodecForTest(b, cc)`. Set the codec on the parent of a mounted router; a router given to `MountRouter` or `HostRouter` calls `sub.CookieCodec(cc)` itself. A function value of `NewCookieCodec` becomes `func(k []byte) *router.CookieCodec { return router.NewCookieCodec(k) }`. A test that expected a `Max-Age=0` `_flash` line after an add and a read in one request now expects no `_flash` line.

```sh
grep -rnE '\.AddFlash\(|\.Flashes\(|SignedCookie\(|NewCookieCodec' --include='*.go' .
```

#### Base.Cookie

```go
// Before
ck, err := c.Cookie("sid")
if err != nil || ck.Value == "" {
	return router.ErrUnauthorized
}
use(ck.Value)

// After
v := c.Cookie("sid")
if v == "" {
	return router.ErrUnauthorized
}
use(v)
```

Code that needs the `*http.Cookie` or `http.ErrNoCookie` calls `c.Request().Cookie(name)`, which has the old shape. A request cookie carries only a name and a value, so nothing is lost.

```sh
grep -rn '\.Cookie(' --include='*.go' .
```

#### BodyLimit

Set `BodyLimit` to the cap Bind should enforce on the routes it covers, or drop a global `BodyLimit` and keep `r.MaxBodyBytes` as the default. `BodyLimit(0)` now also replaces `MaxBodyBytes`, so pass the router's own value where that matters. Behind `Decompress`, set `BodyLimit` to the largest expanded body the route accepts. Read the raw body before `BodyLimit` if a middleware needs it uncapped.

```go
// Before: Bind read at most 1 MiB, the smaller cap.
r.MaxBodyBytes(1 << 20)
r.Use(middleware.BodyLimit[C](10 << 20))

// After: the same code lets Bind read 10 MiB. Keep 1 MiB as the default and raise it on one route.
r.MaxBodyBytes(1 << 20)
r.With(middleware.BodyLimit[C](10 << 20)).POST("/uploads", upload)
```

```sh
grep -rnE 'BodyLimit|MaxBodyBytes' --include='*.go' .
```

#### ParamAs and BindPath

A malformed path value now answers 404, and a name the route does not have answers 500. Where a client must still see 400, map it yourself:

```go
// Before: a malformed id answered 400.
id, err := c.ParamAs[int64]("id")
if err != nil {
	return err
}

// After: the same code answers 404. For a 400:
id, err := c.ParamAs[int64]("id")
if errors.Is(err, router.ErrNotFound) {
	return router.ErrBadRequest.WithMessage("the id is not a number")
}
```

Read a parameter the route may lack with `c.ParamOK(name)` or `c.ParamAsDefault(name, def)`. The fields of a `BindPath` error are in the cause: `router.FieldErrorsOf(err)`.

```sh
grep -rnE 'ParamAs|BindPath' --include='*.go' .
```

#### Bind

A Validator that relied on the stock 422 returns it itself. A type assertion on the Bind error becomes `errors.AsType` or `StatusOf`, and message matching becomes `FieldErrorsOf`.

```go
// Before
func (u *User) Validate() error {
	if taken(u.Name) {
		return router.ErrConflict // answered 422
	}
	return nil
}
if he, ok := err.(*router.HTTPError); ok && strings.Contains(he.Message, "cannot unmarshal") { … }

// After
func (u *User) Validate() error {
	if taken(u.Name) {
		return router.ErrUnprocessableEntity.WithError(router.ErrConflict) // keeps the 422; return ErrConflict for a 409
	}
	return nil
}
if he, ok := errors.AsType[*router.HTTPError](err); ok { … }
for _, fe := range router.FieldErrorsOf(err) { … } // fe.Field is the JSON path, such as events[3].amount
```

The parser text of a syntax error stays in `Err`: `errors.AsType[*jsontext.SyntacticError](err)`, or `router.ErrorHandler[C](true)` in development. Check a `*T` value for nil after a Bind or `ParseValue` error before you dereference it. For strict `strconv` booleans that refuse `on` and `off`, use a named type that implements `encoding.TextUnmarshaler`. Code that reads `req.Form` directly after a router form read calls `req.ParseForm()` first, or uses `req.FormValue`.

```sh
grep -rnE 'err\.\(\*router\.HTTPError\)|malformed JSON body|cannot unmarshal|func \(.*\) Validate\(\) error' --include='*.go' .
```

#### Route metadata

```go
// Before
if rt.Meta == perm { … }
r.Meta(nil).GET("/health", health)

// After
if slices.Contains(rt.Meta, perm) { … }
last := rt.Meta[len(rt.Meta)-1]                  // the nearest value
p, ok := router.MetaAs[Permission](c)            // the same, while the route answers
r.GET("/health", health)                         // outside the Meta scope
```

Compare whole `Route` values with `reflect.DeepEqual`. A method value stored as `func(any) *Router[C]` becomes `func(...any) *Router[C]`. Register a route outside a Meta scope when it must not carry the outer value.

```sh
grep -rnE '\.Meta\(nil\)|\.Meta\(\)|\.Meta ==|\.Meta !=|Meta\(func|func\(any\) \*router\.Router' --include='*.go' .
```

#### The Context interface and SetContext

A context type that declares its own `SetContext`, `SetBodyLimit` or `RouteMeta` with another signature renames it. Replace the `SetRequest(WithContext(...))` dance with `SetContext`, and derive the new context from the request context, never from `c`:

```go
// Before
c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), key, v)))
c.SetRequest(c.Request().WithContext(context.WithValue(c, key, v))) // now panics

// After
c.SetContext(context.WithValue(c.Request().Context(), key, v))
```

```sh
grep -rnE 'SetRequest\(.*WithContext|func \(.*\) (SetContext|SetBodyLimit|RouteMeta)\(' --include='*.go' .
```

#### routertest

```go
// Before
res.AssertStatus(t, http.StatusOK)
res.AssertBody(t, "ok")
res.AssertHeader(t, "Content-Type", "text/plain")

// After
res.Expect(t).
	Status(http.StatusOK).
	Body("ok").
	Header("Content-Type", "text/plain")
```

`Body` and `Header` no longer stop the test, so put `Status` first when later lines depend on it. Where a handler must keep a context that never ends, pass `routertest.WithRequest(routertest.Request(http.MethodGet, "/"))` or `routertest.WithTarget(method, target, routertest.Context(context.Background()))` to `NewContext`.

```sh
grep -rnE '\.Assert(Status|Body|Header)\(|routertest\.NewContext\(' --include='*.go' .
```

#### serve

Build `Config` with named fields. Provide a TLS certificate through `TLSConfig`, a `Cert*` option or `OnServer`; code that filled `srv.TLSConfig` from `OnListen` moves into `OnServer`. Check a Run error for nil rather than matching its text.

```sh
grep -rnE 'serve\.Config\{[^}N]|OnListen|Run needs' --include='*.go' .
```

A public and a private server can now run as one `serve.RunAll` call, the public one first with `DrainDelay` and `OnDrain`, so the private one keeps answering probes through the public drain.

## [0.1.0] - 2026-09-02

The first release. See the [v0.1.0 tag](https://github.com/dmitrymomot/go-router/releases/tag/v0.1.0).

[0.2.0]: https://github.com/dmitrymomot/go-router/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/dmitrymomot/go-router/releases/tag/v0.1.0
