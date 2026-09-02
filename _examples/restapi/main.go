// Command restapi is a JSON API on top of go-router: binding with validation,
// domain errors that carry their own status, a JSON error handler, and a
// server that drains on Ctrl-C.
package main

import (
	"context"
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

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := serve.Run(ctx, newRouter(NewStore()), serve.Config{
		Addr:            ":8080",
		ReadTimeout:     15 * time.Second,
		WriteTimeout:    15 * time.Second,
		ShutdownTimeout: 10 * time.Second,
	}); err != nil {
		slog.Error("the server stopped", "error", err)
		os.Exit(1)
	}
}

func newRouter(store *Store) *router.Router[*Context] {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context {
		return &Context{Store: store}
	})

	// Every failure, from a bad body to a panic, ends up here.
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
		middleware.RateLimit(middleware.NewMemoryStore[*Context](10, 20, time.Minute)),
		middleware.CORSWithConfig[*Context](middleware.CORSConfig{
			AllowOrigins: []string{"https://app.example.com"},
		}),
	)

	routes(r)
	return r
}
