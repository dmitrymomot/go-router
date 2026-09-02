// Command restapi is a JSON API on top of go-router: a mounted sub-router,
// binding with validation, domain errors that carry their own status, a JSON
// error handler, and a server that drains on Ctrl-C.
package main

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
	"github.com/dmitrymomot/go-router/serve"
)

const healthPath = "/healthz"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	apiKey := os.Getenv("API_KEY")
	if apiKey == "" {
		// Better a key the operator can read from the log than one compiled in.
		apiKey = rand.Text()
		slog.Info("no API_KEY in the environment, so this run made one", "key", apiKey)
	}

	r := newRouter(NewStore(), apiKey)
	for _, rt := range r.Routes() {
		slog.Info("route", "method", rt.Method, "pattern", rt.Pattern)
	}

	if err := serve.Run(ctx, r, serve.Config{
		Addr:            ":8080",
		ReadTimeout:     15 * time.Second,
		WriteTimeout:    15 * time.Second,
		ShutdownTimeout: 10 * time.Second,
	}); err != nil {
		slog.Error("the server stopped", "error", err)
		os.Exit(1)
	}
}

func newRouter(store *Store, apiKey string) *router.Router[*Context] {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context {
		return &Context{Store: store}
	})

	// Every failure, from a bad body to a panic, ends up here. These settings
	// belong to the root: a mounted router is refused if it carries them.
	r.ErrorHandler(writeError)
	r.MaxBodyBytes(1 << 20)

	// The order is the order of the request: recover the panic, name the
	// request, learn who sent it, log it, then decide whether to serve it.
	r.Use(
		middleware.Recover[*Context],
		middleware.RequestID[*Context],
		middleware.RealIPWithConfig[*Context](middleware.RealIPConfig{
			Headers: []string{router.HeaderXForwardedFor},
		}),
		middleware.Logger[*Context],
		middleware.RateLimitWithConfig(middleware.RateLimitConfig[*Context]{
			Store: middleware.NewMemoryStore[*Context](10, 20, time.Minute),
			// A load balancer polls the health of this service far harder than
			// any client, and must never be turned away.
			Skip: func(c router.Context) bool { return c.Request().URL.Path == healthPath },
		}),
		middleware.CORSWithConfig[*Context](middleware.CORSConfig{
			AllowOrigins: []string{"https://app.example.com"},
		}),
	)

	r.GET(healthPath, func(c *Context) error { return c.NoContent(http.StatusNoContent) })

	// The version lives at the mount and nowhere else. A v2 is another line.
	r.Mount("/v1", usersAPI(apiKey))

	return r
}
