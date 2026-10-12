package middleware_test

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
	"github.com/dmitrymomot/go-router/routertest"
)

func ExampleETag() {
	r := newAPI()
	r.Use(middleware.ETag[*Context])
	r.GET("/report", func(c *Context) error { return c.String(http.StatusOK, "totals") })

	first := routertest.Get(r, "/report")
	again := routertest.Get(r, "/report", routertest.Header("If-None-Match", first.Header.Get("ETag")))
	fmt.Println(first.StatusCode, again.StatusCode)
	// Output: 200 304
}

func ExampleNoCache() {
	r := newAPI()
	r.Use(middleware.NoCache[*Context])
	r.GET("/me", sayOK)

	res := routertest.Get(r, "/me")
	fmt.Println(res.Header.Get("Cache-Control"))
	// Output: no-cache, no-store, no-transform, must-revalidate, private, max-age=0
}

func ExampleThrottle() {
	r := newAPI()
	// At most 10 reports run at once; 20 more wait up to 2 seconds for a slot,
	// and the rest get a 503 with a Retry-After of 5 seconds.
	r.Use(middleware.ThrottleWithConfig(middleware.ThrottleConfig[*Context]{
		Limit:          10,
		Backlog:        20,
		BacklogTimeout: 2 * time.Second,
		Status:         http.StatusServiceUnavailable,
		RetryAfter:     5 * time.Second,
	}))
	r.GET("/report", sayOK)

	fmt.Println(routertest.Get(r, "/report").StatusCode)
	// Output: 200
}

func ExampleHeartbeat() {
	r := newAPI()
	// Mount it at the root, so it also answers a path with no route.
	r.Use(middleware.Heartbeat[*Context]("/ping"))
	r.GET("/", sayOK)

	res := routertest.Get(r, "/ping")
	fmt.Println(res.StatusCode, res.String())
	// Output: 200 .
}

func ExampleStripSlashes() {
	r := newAPI()
	r.GET("/users", sayOK)

	// StripSlashes goes in front of the router, which picks the route.
	h := middleware.StripSlashes(r)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/", nil))
	fmt.Println(rec.Code)
	// Output: 200
}

func ExampleProfiler() {
	r := newAPI()
	// The profiler exposes the internals of the process: guard it.
	r.Route("/debug", func(g *router.Router[*Context]) {
		g.Use(middleware.BasicAuth(func(_ *Context, user, pass string) (bool, error) {
			return middleware.SecureCompare(user, "ops") && middleware.SecureCompare(pass, "s3cret"), nil
		}))
		g.MountHandler("/", middleware.Profiler())
	})

	anon := routertest.Get(r, "/debug/vars")
	authed := routertest.Get(r, "/debug/vars", routertest.Header("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("ops:s3cret"))))
	fmt.Println(anon.StatusCode, authed.StatusCode)
	// Output: 401 200
}

func ExampleDeprecation() {
	r := newAPI()
	r.Use(middleware.Deprecation(middleware.DeprecationConfig[*Context]{
		Deprecated: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Sunset:     time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		Link:       "https://example.com/migrate",
	}))
	r.GET("/v1/users", sayOK)

	h := routertest.Get(r, "/v1/users").Header
	fmt.Println(h.Get("Deprecation"))
	fmt.Println(h.Get("Sunset"))
	fmt.Println(h.Get("Link"))
	// Output:
	// @1767225600
	// Fri, 01 Jan 2027 00:00:00 GMT
	// <https://example.com/migrate>; rel="deprecation"
}

func ExampleMethodOverride() {
	r := newAPI()
	r.DELETE("/items/1", sayOK)

	// MethodOverride goes in front of the router, which picks the route.
	h := middleware.MethodOverride(r)
	form := httptest.NewRequest(http.MethodPost, "/items/1", strings.NewReader("_method=DELETE"))
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, form)
	fmt.Println(rec.Code)
	// Output: 200
}

func ExampleAllowContentType() {
	r := newAPI()
	r.Use(middleware.AllowContentType[*Context]("application/json"))
	r.POST("/items", sayOK)

	res := routertest.Do(r, http.MethodPost, "/items", routertest.Body("text/plain", strings.NewReader("hi")))
	fmt.Println(res.StatusCode)
	// Output: 415
}
