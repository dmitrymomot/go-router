// Command wschat is a chat room over WebSocket: one page, a few lines of
// JavaScript, and package ws on the server.
package main

import (
	"context"
	"embed"
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
}

type Ctx = *Context

const addr = "localhost:8080"

//go:embed static
var files embed.FS

func main() {
	// SIGTERM is what a container runtime sends.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := serve.Run(ctx, newRouter(newRoom()), serve.Config{
		Addr:              addr,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       time.Minute,
		ShutdownTimeout:   5 * time.Second,
		// A hijacked connection outlives the timeouts of the server: package
		// ws clears them and bounds each write with its own WriteTimeout. When
		// the drain starts, every socket gets a close frame with code 1012,
		// and the page reconnects to the next server.
		OnListen: func(a net.Addr) { slog.Info("wschat is listening", "url", "http://"+a.String()) },
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
		// The page runs no inline script or style, so the policy can refuse
		// both. 'self' in connect-src covers ws:// and wss:// to this host.
		middleware.SecureWithConfig(middleware.SecureConfig[Ctx]{
			ContentSecurityPolicy: "default-src 'self'; connect-src 'self'; frame-ancestors 'none'",
		}),
	)

	r.GET("/", asset("static/index.html", router.MIMETextHTMLCharsetUTF8))
	r.GET("/app.js", asset("static/app.js", "text/javascript; charset=utf-8"))
	r.GET("/style.css", asset("static/style.css", "text/css; charset=utf-8"))
	r.GET("/ws", socket)

	return r
}

// asset serves one embedded file. A real site with more of them would mount
// package static.
func asset(name, contentType string) router.HandlerFunc[Ctx] {
	data, err := files.ReadFile(name)
	if err != nil {
		panic(err) // the file is embedded, so this is a typo in the name
	}
	return func(c Ctx) error { return c.Blob(http.StatusOK, contentType, data) }
}
