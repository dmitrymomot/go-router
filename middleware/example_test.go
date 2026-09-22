package middleware_test

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
	"github.com/dmitrymomot/go-router/routertest"
)

// Context is this application's own type. Any struct that embeds router.Base
// will do.
type Context struct {
	router.Base
}

func newAPI() *router.Router[*Context] {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context {
		return new(Context)
	})
	// The examples below answer some requests with an error, and the router
	// logs every one. Keep that out of the example output.
	r.Logger(slog.New(slog.DiscardHandler))
	return r
}

func sayOK(c *Context) error { return c.String(http.StatusOK, "ok") }

func ExampleCSRF() {
	r := newAPI()
	r.Use(middleware.CSRF[*Context])

	// The token reaches the page through the context, and comes back in the
	// _csrf form field. It is also the value of the _csrf cookie.
	r.GET("/form", func(c *Context) error {
		return c.String(http.StatusOK, fmt.Sprintf(`<input name="_csrf" value=%q>`,
			middleware.CSRFTokenFrom(c)))
	})
	r.POST("/save", sayOK)

	form := routertest.Get(r, "/form")
	cookie := form.Cookies()[0]

	saved := routertest.Do(r, http.MethodPost, "/save",
		routertest.Cookie(cookie),
		routertest.FormBody(url.Values{"_csrf": {cookie.Value}}))
	forged := routertest.Do(r, http.MethodPost, "/save", routertest.Cookie(cookie))

	fmt.Println(cookie.Name, strings.Contains(form.String(), cookie.Value))
	fmt.Println(saved.StatusCode, saved.String())
	fmt.Println(forged.StatusCode, forged.String())
	// Output:
	// _csrf true
	// 200 ok
	// 403 invalid CSRF token
}

func ExampleRateLimit() {
	// One request per second, one in the bucket. Build the store once and
	// share it: a store per route counts each route separately.
	store := middleware.NewRateLimitMemoryStore(1, 1, time.Minute)

	r := newAPI()
	// The key is ClientIP, so RealIP belongs in front of this behind a proxy.
	r.Use(middleware.RateLimit[*Context](store))
	r.GET("/ping", sayOK)

	first := routertest.Get(r, "/ping")
	second := routertest.Get(r, "/ping")

	fmt.Println(first.StatusCode, first.String())
	fmt.Println(second.StatusCode, second.Header.Get(router.HeaderRetryAfter))
	// Output:
	// 200 ok
	// 429 1
}

func ExampleRealIPWithConfig() {
	report := func(mw router.Middleware[*Context]) string {
		r := newAPI()
		r.Use(mw)
		r.GET("/", func(c *Context) error {
			return c.String(http.StatusOK, middleware.ClientIP(c)+" "+c.Scheme())
		})
		return routertest.Get(r, "/",
			routertest.Header(router.HeaderXForwardedFor, "203.0.113.9"),
			routertest.Header(router.HeaderXForwardedProto, "https")).String()
	}

	// The bare middleware names no header and trusts no public peer, so it
	// strips every forwarding header and the connection stands.
	fmt.Println(report(middleware.RealIP[*Context]))

	// A header counts only when it is named and the peer is a trusted proxy.
	// The test request arrives from 192.0.2.1, which no default trusts. The
	// scheme of a trusted proxy is kept without being named.
	proxies := middleware.NewTrustSet(
		middleware.TrustPrefix(netip.MustParsePrefix("192.0.2.0/24")))
	fmt.Println(report(middleware.RealIPWithConfig(middleware.RealIPConfig[*Context]{
		Headers: []string{router.HeaderXForwardedFor},
		Trust:   proxies,
	})))

	// DropProto takes the scheme from the connection alone.
	fmt.Println(report(middleware.RealIPWithConfig(middleware.RealIPConfig[*Context]{
		Headers:   []string{router.HeaderXForwardedFor},
		Trust:     proxies,
		DropProto: true,
	})))
	// Output:
	// 192.0.2.1 http
	// 203.0.113.9 https
	// 203.0.113.9 http
}

func ExampleClientAddr() {
	r := newAPI()
	r.Use(middleware.RealIPWithConfig(middleware.RealIPConfig[*Context]{
		Headers: []string{router.HeaderXForwardedFor},
		Trust: middleware.NewTrustSet(
			middleware.TrustPrefix(netip.MustParsePrefix("192.0.2.0/24"))),
	}))
	r.GET("/", func(c *Context) error {
		addr, ok := middleware.ClientAddr(c)
		if !ok {
			return router.ErrBadRequest
		}
		return c.String(http.StatusOK, fmt.Sprint(addr, " ", addr.Is4()))
	})

	// The proxy wrote an IPv4-mapped address, which comes back unmapped.
	fmt.Println(routertest.Get(r, "/",
		routertest.Header(router.HeaderXForwardedFor, "::ffff:203.0.113.9")).String())
	// Output:
	// 203.0.113.9 true
}

func ExampleKeyAuth() {
	r := newAPI()
	// The default source is the Authorization header, less the "Bearer "
	// prefix. KeyAuthWithConfig panics without a Validator.
	r.Use(middleware.KeyAuth(func(_ *Context, key string) (bool, error) {
		return middleware.SecureCompare(key, "s3cret"), nil
	}))
	r.GET("/vault", sayOK)

	good := routertest.Get(r, "/vault", routertest.Header(router.HeaderAuthorization, "Bearer s3cret"))
	bad := routertest.Get(r, "/vault", routertest.Header(router.HeaderAuthorization, "Bearer nope"))
	none := routertest.Get(r, "/vault")

	fmt.Println(good.StatusCode, good.String())
	fmt.Println(bad.StatusCode, bad.String())
	fmt.Println(none.StatusCode, none.Header.Get(router.HeaderWWWAuthenticate))
	// Output:
	// 200 ok
	// 401 invalid key
	// 401 Bearer
}

func ExampleTimeoutWithConfig() {
	r := newAPI()
	r.Use(middleware.TimeoutWithConfig(middleware.TimeoutConfig[*Context]{
		Duration: time.Millisecond,
	}))
	// The deadline cancels the request context; it does not stop the
	// goroutine. A handler that ignores c.Done() runs to the end regardless.
	r.GET("/slow", func(c *Context) error {
		<-c.Done()
		return c.Err()
	})

	res := routertest.Get(r, "/slow")
	fmt.Println(res.StatusCode, res.String())
	// Output:
	// 503 Service Unavailable
}

func ExampleMinDuration() {
	r := newAPI()
	// The answer is the same whether or not the address has an account, and
	// the floor makes it take the same time, so neither gives the account away.
	r.With(middleware.MinDuration[*Context](20*time.Millisecond)).POST("/login",
		func(c *Context) error {
			return c.String(http.StatusOK, "check your inbox")
		})

	start := time.Now()
	res := routertest.Do(r, http.MethodPost, "/login",
		routertest.FormBody(url.Values{"email": {"nobody@example.com"}}))
	fmt.Println(res.StatusCode, res.String(), time.Since(start) >= 20*time.Millisecond)
	// Output:
	// 200 check your inbox true
}

func ExampleMinDurationWithConfig() {
	r := newAPI()
	r.Route("/login", func(g *router.Router[*Context]) {
		// The form itself gives nothing away, so only the POST waits.
		g.Use(middleware.MinDurationWithConfig(middleware.MinDurationConfig[*Context]{
			Duration: 20 * time.Millisecond,
			Skip:     func(c *Context) bool { return c.Request().Method == http.MethodGet },
		}))
		g.GET("/", sayOK)
		g.POST("/", sayOK)
	})

	start := time.Now()
	res := routertest.Do(r, http.MethodPost, "/login")
	fmt.Println(res.StatusCode, "held:", time.Since(start) >= 20*time.Millisecond)
	// Output:
	// 200 held: true
}

func ExampleGzip() {
	r := newAPI()
	r.Use(middleware.Gzip[*Context])
	r.GET("/short", func(c *Context) error { return c.String(http.StatusOK, "small") })
	// Only a body over DefaultGzipMinLength is worth the compression.
	r.GET("/long", func(c *Context) error {
		return c.String(http.StatusOK, strings.Repeat("a", 2000))
	})

	accept := routertest.Header(router.HeaderAcceptEncoding, "gzip")
	short := routertest.Get(r, "/short", accept)
	long := routertest.Get(r, "/long", accept)

	fmt.Printf("short: %q %d\n", short.Header.Get(router.HeaderContentEncoding), len(short.Body))
	fmt.Printf("long: %q under 2000: %t\n",
		long.Header.Get(router.HeaderContentEncoding), len(long.Body) < 2000)
	// Output:
	// short: "" 5
	// long: "gzip" under 2000: true
}

func ExampleParseForm() {
	r := newAPI()
	r.MaxBodyBytes(32)
	r.Use(middleware.ParseForm[*Context])
	calls := 0
	r.POST("/toggle", func(c *Context) error {
		calls++
		// Without ParseForm, an oversized form would read as empty here and
		// switch the setting off.
		return c.String(http.StatusOK, fmt.Sprintf("on=%t", c.FormValue("on") == "on"))
	})

	for _, body := range []string{"on=on", "on=on&note=" + strings.Repeat("x", 64)} {
		res := routertest.Do(r, http.MethodPost, "/toggle",
			routertest.Body(router.MIMEApplicationForm, strings.NewReader(body)))
		fmt.Println(res.StatusCode, res.String())
	}
	fmt.Println("handler calls:", calls)
	// Output:
	// 200 on=true
	// 413 Request Entity Too Large
	// handler calls: 1
}

func ExampleBodyLimit() {
	r := newAPI()
	// The default for every route, and more for the one that takes uploads.
	r.MaxBodyBytes(64)
	save := func(c *Context) error {
		if _, err := c.BindJSON[map[string]string](); err != nil {
			return err
		}
		return c.NoContent(http.StatusOK)
	}
	r.With(middleware.BodyLimit[*Context](1<<20)).POST("/uploads", save)
	r.POST("/notes", save)

	body := `{"text":"` + strings.Repeat("x", 200) + `"}`
	for _, target := range []string{"/uploads", "/notes"} {
		res := routertest.Do(r, http.MethodPost, target,
			routertest.Body(router.MIMEApplicationJSON, strings.NewReader(body)))
		fmt.Println(target, res.StatusCode)
	}
	// Output:
	// /uploads 200
	// /notes 413
}

func ExampleDecompress() {
	r := newAPI()
	// BodyLimit bounds the bytes on the wire and what Bind reads of their
	// expansion; MaxDecompressedSize bounds the expansion for any reader. A
	// zip bomb needs both.
	r.Use(
		middleware.BodyLimit[*Context](1<<20),
		middleware.DecompressWithConfig(middleware.DecompressConfig[*Context]{
			MaxDecompressedSize: 1 << 20,
		}),
	)
	r.POST("/echo", func(c *Context) error {
		body, err := c.BindJSON[map[string]string]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, body["say"])
	})

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	//nolint:errcheck // A bytes.Buffer never fails.
	zw.Write([]byte(`{"say":"hello"}`))
	_ = zw.Close()

	res := routertest.Do(r, http.MethodPost, "/echo",
		routertest.Body(router.MIMEApplicationJSON, bytes.NewReader(buf.Bytes())),
		routertest.Header(router.HeaderContentEncoding, "gzip"))
	fmt.Println(res.StatusCode, res.String())
	// Output:
	// 200 hello
}

func ExampleSecureWithConfig() {
	r := newAPI()
	r.Use(middleware.SecureWithConfig(middleware.SecureConfig[*Context]{
		// An empty field keeps the default. SecureOmit drops the header.
		FrameOptions:          middleware.SecureOmit,
		ContentSecurityPolicy: "default-src 'self'",
	}))
	r.GET("/", sayOK)

	res := routertest.Get(r, "/")
	fmt.Printf("%q\n", res.Header.Get(router.HeaderXFrameOptions))
	fmt.Println(res.Header.Get(router.HeaderXContentTypeOptions))
	fmt.Println(res.Header.Get(router.HeaderContentSecurityPolicy))
	// Output:
	// ""
	// nosniff
	// default-src 'self'
}

func ExampleCORSWithConfig() {
	r := newAPI()
	// A wildcard origin beside AllowCredentials panics: no browser honors it.
	r.Use(middleware.CORSWithConfig(middleware.CORSConfig[*Context]{
		AllowOrigins:     []string{"https://app.example.com"},
		AllowCredentials: true,
		MaxAge:           time.Hour,
	}))
	r.POST("/orders", sayOK)

	preflight := routertest.Do(r, http.MethodOptions, "/orders",
		routertest.Header(router.HeaderOrigin, "https://app.example.com"),
		routertest.Header(router.HeaderAccessControlRequestMethod, http.MethodPost))
	other := routertest.Do(r, http.MethodPost, "/orders",
		routertest.Header(router.HeaderOrigin, "https://evil.example.com"))

	fmt.Println(preflight.StatusCode,
		preflight.Header.Get(router.HeaderAccessControlAllowOrigin),
		preflight.Header.Get(router.HeaderAccessControlMaxAge))
	fmt.Printf("%d %q\n", other.StatusCode,
		other.Header.Get(router.HeaderAccessControlAllowOrigin))
	// Output:
	// 204 https://app.example.com 3600
	// 200 ""
}

func ExampleRequestIDWithConfig() {
	r := newAPI()
	r.Use(middleware.RequestIDWithConfig(middleware.RequestIDConfig[*Context]{
		// A real program leaves Generator nil, for a UUIDv7 per request.
		Generator: func() string { return "req-1" },
		// IgnoreInbound refuses the id a client sends, which nothing verifies.
		IgnoreInbound: true,
	}))
	r.GET("/", func(c *Context) error {
		return c.String(http.StatusOK, middleware.RequestIDFrom(c))
	})

	res := routertest.Get(r, "/", routertest.Header(router.HeaderXRequestID, "forged"))
	fmt.Println(res.Header.Get(router.HeaderXRequestID), res.String())
	// Output:
	// req-1 req-1
}

func ExampleLoggerWithConfig() {
	// Keep the record short and fixed, so the example output does not move.
	keep := func(_ []string, a slog.Attr) slog.Attr {
		switch a.Key {
		case slog.LevelKey, slog.MessageKey, "status":
			return a
		}
		return slog.Attr{}
	}

	r := newAPI()
	r.Use(middleware.LoggerWithConfig(middleware.LoggerConfig[*Context]{
		Logger: slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{ReplaceAttr: keep})),
	}))
	r.GET("/ok", sayOK)
	r.GET("/gone", func(*Context) error { return router.ErrGone })
	r.GET("/broken", func(*Context) error { return router.ErrInternalServerError })

	// The level follows the status class: Info, then Warn, then Error.
	for _, path := range []string{"/ok", "/gone", "/broken"} {
		routertest.Get(r, path)
	}
	// Output:
	// level=INFO msg=request status=200
	// level=WARN msg=request status=410
	// level=ERROR msg=request status=500
}

func ExampleIdempotency() {
	// Build the store once and share it across the routes it covers.
	store := middleware.NewIdempotencyMemoryStore(time.Hour)

	r := newAPI()
	charges := 0
	r.With(middleware.Idempotency[*Context](store)).POST("/pay", func(c *Context) error {
		charges++
		return c.String(http.StatusCreated, fmt.Sprintf("payment %d", charges))
	})

	// The client lost the first answer and sent the same request again.
	for range 2 {
		res := routertest.Do(r, http.MethodPost, "/pay",
			routertest.Header(router.HeaderIdempotencyKey, "k1"),
			routertest.FormBody(url.Values{"amount": {"5"}}))
		fmt.Println(res.StatusCode, res.String())
	}
	fmt.Println("charged", charges)
	// Output:
	// 201 payment 1
	// 201 payment 1
	// charged 1
}

func ExampleIdempotencyWithConfig() {
	r := newAPI()
	charges := 0
	// A plain HTML form carries its key in a hidden field, and every form on
	// these routes must carry one.
	r.With(middleware.IdempotencyWithConfig(middleware.IdempotencyConfig[*Context]{
		Store:    middleware.NewIdempotencyMemoryStore(time.Hour),
		Sources:  []middleware.TokenSource{middleware.FromForm("_request")},
		Required: true,
	})).POST("/pay", func(c *Context) error {
		charges++
		return c.String(http.StatusOK, fmt.Sprintf("paid %s", c.FormValue("amount")))
	})

	submit := func(form url.Values) {
		res := routertest.Do(r, http.MethodPost, "/pay", routertest.FormBody(form))
		fmt.Println(res.StatusCode, res.String())
	}
	submit(url.Values{"_request": {"r1"}, "amount": {"5"}})
	submit(url.Values{"_request": {"r1"}, "amount": {"5"}}) // a double click
	submit(url.Values{"_request": {"r1"}, "amount": {"9"}}) // the same key, another request
	submit(url.Values{"amount": {"5"}})
	fmt.Println("charged", charges)
	// Output:
	// 200 paid 5
	// 200 paid 5
	// 422 the idempotency key was used for a different request
	// 400 the request needs an Idempotency-Key
	// charged 1
}

func ExampleIdempotency_json() {
	// The default fingerprint covers the body, so a key reused with another
	// body is refused.
	r := newAPI()
	r.With(middleware.Idempotency[*Context](middleware.NewIdempotencyMemoryStore(time.Hour))).POST("/pay", func(c *Context) error {
		in, err := c.Bind[struct {
			Amount int `json:"amount"`
		}]()
		if err != nil {
			return err
		}
		return c.String(http.StatusCreated, fmt.Sprintf("paid %d", in.Amount))
	})

	for _, body := range []string{`{"amount":5}`, `{"amount":5}`, `{"amount":9}`} {
		res := routertest.Do(r, http.MethodPost, "/pay",
			routertest.Header(router.HeaderIdempotencyKey, "k1"),
			routertest.Body(router.MIMEApplicationJSON, strings.NewReader(body)))
		fmt.Println(res.StatusCode, res.String())
	}
	// Output:
	// 201 paid 5
	// 201 paid 5
	// 422 the idempotency key was used for a different request
}

func ExampleNewIdempotencyMemoryStoreWithConfig() {
	// Keep a key for a day, and at most 10000 of them. A full store drops the
	// oldest key whose answer it holds.
	store := middleware.NewIdempotencyMemoryStoreWithConfig(middleware.IdempotencyMemoryStoreConfig{
		ExpiresIn:  24 * time.Hour,
		MaxEntries: 10000,
	})

	passwords := map[string]string{"alice": "a-pass", "bob": "b-pass"}
	signedIn := func(c *Context) string {
		user, _, _ := c.Request().BasicAuth()
		return user
	}

	r := newAPI()
	r.Use(middleware.BasicAuth(func(_ *Context, user, pass string) (bool, error) {
		want, ok := passwords[user]
		return ok && middleware.SecureCompare(pass, want), nil
	}))
	r.Use(middleware.IdempotencyWithConfig(middleware.IdempotencyConfig[*Context]{
		Store: store,
		// Each user has keys of their own. BasicAuth in front checked the
		// password, so the name is that of the signed-in user.
		Client: signedIn,
	}))
	r.POST("/orders", func(c *Context) error {
		return c.String(http.StatusCreated, fmt.Sprintf("order for %s", signedIn(c)))
	})

	for _, user := range []string{"alice", "bob"} {
		res := routertest.Do(r, http.MethodPost, "/orders",
			routertest.Header(router.HeaderIdempotencyKey, "k1"),
			func(req *http.Request) { req.SetBasicAuth(user, passwords[user]) })
		fmt.Println(res.StatusCode, res.String())
	}
	// Output:
	// 201 order for alice
	// 201 order for bob
}
