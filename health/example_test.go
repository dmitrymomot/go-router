package health_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/health"
	"github.com/dmitrymomot/go-router/routertest"
)

func Example() {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })

	// db.PingContext fits Func as it stands; here the cache is down.
	health.Mount(r, health.Config{
		Checks: []health.Check{
			{Name: "postgres", Func: func(context.Context) error { return nil }},
			{Name: "cache", Func: func(context.Context) error { return errors.New("down") }, NonCritical: true},
		},
	})

	live := routertest.Get(r, "/livez")
	fmt.Println(live.StatusCode, live.String())
	ready := routertest.Get(r, "/readyz")
	fmt.Println(ready.StatusCode, ready.String())
	// Output:
	// 200 {"status":"ok"}
	// 200 {"status":"degraded","checks":[{"name":"postgres","ok":true,"critical":true},{"name":"cache","ok":false,"critical":false}]}
}

func ExampleHandler() {
	// A private server serves the probes, beside the public one:
	//
	//	serve.RunAll(ctx,
	//		serve.Server{Handler: app, Config: serve.Config{Addr: ":8080", DrainDelay: 5 * time.Second}},
	//		serve.Server{Handler: probes, Config: serve.Config{Addr: ":9090"}},
	//	)
	//
	// Readiness there answers 503 as soon as the public server begins to stop.
	probes := health.Handler(health.Config{
		ExposeErrors: true, // The private network alone reaches it.
		Checks: []health.Check{
			{Name: "postgres", Func: func(context.Context) error { return errors.New("connection refused") }},
		},
	})

	res := routertest.Get(probes, "/readyz")
	fmt.Println(res.StatusCode, res.String())
	// Output:
	// 503 {"status":"unavailable","checks":[{"name":"postgres","error":"connection refused","ok":false,"critical":true}]}
}
