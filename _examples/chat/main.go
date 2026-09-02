package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
	"github.com/dmitrymomot/go-router/serve"
)

type Context struct {
	router.Base
	Room *room
	User string
}

type Ctx = *Context

const addr = "localhost:8080"

const maxBodyBytes = 8 << 10

func main() {
	// SIGTERM is what a container runtime sends.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rm := newRoom()
	// Every open stream is blocked on the room, so the room has to close
	// before the drain starts. A drain that waits for a stream never ends.
	go func() {
		<-ctx.Done()
		rm.close()
	}()

	if err := serve.Run(ctx, newRouter(rm), serve.Config{
		Addr:              addr,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       time.Minute,
		ShutdownTimeout:   5 * time.Second,
		// No WriteTimeout: it would cut every stream at the deadline.
		OnListen: func(a net.Addr) { slog.Info("chat is listening", "url", "http://"+a.String()) },
		// Config carries what a server usually needs. OnServer reaches the
		// rest of http.Server.
		OnServer: func(srv *http.Server) error {
			srv.MaxHeaderBytes = 16 << 10
			return nil
		},
	}); err != nil {
		slog.Error("the server stopped", "error", err)
		os.Exit(1)
	}
}

func newRouter(rm *room) *router.Router[Ctx] {
	r := router.New(func(http.ResponseWriter, *http.Request) Ctx {
		return &Context{Room: rm}
	})

	// Logger wraps Recover, so the request line carries the recovered panic.
	r.Use(
		middleware.Logger[Ctx],
		middleware.Recover[Ctx],
		middleware.Secure[Ctx],
		middleware.BodyLimit[Ctx](maxBodyBytes),
		middleware.CSRFWithConfig[Ctx](middleware.CSRFConfig{
			CookieHTTPOnly: true,
			CookieSameSite: http.SameSiteLaxMode,
		}),
	)

	r.GET("/", index)
	r.POST("/join", join)
	r.POST("/leave", leave)

	// The room, mounted as a router of its own. The name check sits at its
	// door, and the /room prefix appears here and in no handler.
	r.Mount("/room", roomRouter())

	return r
}
