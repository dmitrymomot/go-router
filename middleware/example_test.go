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
		return c.Stringf(http.StatusOK, `<input name="_csrf" value=%q>`,
			middleware.CSRFTokenFrom(c))
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
	store := middleware.NewMemoryStore[*Context](1, 1, time.Minute)

	r := newAPI()
	// The key is ClientIP, so RealIP belongs in front of this behind a proxy.
	r.Use(middleware.RateLimit(store))
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
			return c.String(http.StatusOK, middleware.ClientIP(c))
		})
		return routertest.Get(r, "/",
			routertest.Header(router.HeaderXForwardedFor, "203.0.113.9")).String()
	}

	// The bare middleware names no header, so it strips every forwarding
	// header and the peer address stands.
	fmt.Println(report(middleware.RealIP[*Context]))

	// A header counts only when it is named and the peer is a trusted proxy.
	// The test request arrives from 192.0.2.1, which no default trusts.
	fmt.Println(report(middleware.RealIPWithConfig[*Context](middleware.RealIPConfig{
		Headers: []string{router.HeaderXForwardedFor},
		Trust: middleware.NewTrustSet(
			middleware.TrustPrefix(netip.MustParsePrefix("192.0.2.0/24"))),
	})))
	// Output:
	// 192.0.2.1
	// 203.0.113.9
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
	r.Use(middleware.TimeoutWithConfig[*Context](middleware.TimeoutConfig{
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

func ExampleDecompress() {
	r := newAPI()
	// BodyLimit bounds the bytes on the wire; MaxDecompressedSize bounds what
	// they expand into. A zip bomb needs both.
	r.Use(
		middleware.BodyLimit[*Context](1<<20),
		middleware.DecompressWithConfig[*Context](middleware.DecompressConfig{
			MaxDecompressedSize: 1 << 20,
		}),
	)
	r.POST("/echo", func(c *Context) error {
		body, err := c.Bind[map[string]string]()
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
	r.Use(middleware.SecureWithConfig[*Context](middleware.SecureConfig{
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
	r.Use(middleware.CORSWithConfig[*Context](middleware.CORSConfig{
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
	r.Use(middleware.RequestIDWithConfig[*Context](middleware.RequestIDConfig{
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
	r.Use(middleware.LoggerWithConfig[*Context](middleware.LoggerConfig{
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
