// Command tenants is a multi-tenant web app on top of go-router: a landing
// page on the base domain, a signup form that creates a workspace, and one
// route table that serves every workspace on its own subdomain.
//
// lvh.me and every name under it resolve to 127.0.0.1, so the whole thing runs
// with no entry in /etc/hosts.
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
	"github.com/dmitrymomot/go-router/session"
)

// baseDomain is the apex. A workspace named Acme answers on acme.lvh.me.
const baseDomain = "lvh.me"

// tenantHost is the host of every workspace. The route table registers it,
// and links to a workspace fill it in with router.MustExpand.
const tenantHost = "{tenant}." + baseDomain

const addr = "localhost:8080"

const maxBodyBytes = 8 << 10

// Context is what every handler receives. The factory gives it the store. The
// workspace of the subdomain, the session and the signed-in account ride on
// the request context, where middleware of packages tenant, session and user
// puts them.
type Context struct {
	router.Base
	Store *Store
}

type Ctx = *Context

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	r := newRouter(NewStore())

	if err := serve.Run(ctx, r, serve.Config{
		Addr:              addr,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       time.Minute,
		ShutdownTimeout:   5 * time.Second,
		OnListen: func(a net.Addr) {
			_, port, _ := net.SplitHostPort(a.String())
			slog.Info("open the landing page", "url", "http://"+net.JoinHostPort(baseDomain, port))
		},
	}); err != nil {
		slog.Error("the server stopped", "error", err)
		os.Exit(1)
	}
}

func newRouter(store *Store) *router.Router[Ctx] {
	r := router.New(func(http.ResponseWriter, *http.Request) Ctx {
		return &Context{Store: store}
	})

	r.ErrorHandler(renderError)

	r.Use(
		middleware.Logger[Ctx],
		middleware.Recover[Ctx],
		middleware.Secure[Ctx],
		middleware.BodyLimit[Ctx](maxBodyBytes),
		middleware.CSRFWithConfig(middleware.CSRFConfig[Ctx]{
			CookieHTTPOnly: true,
			CookieSameSite: http.SameSiteLaxMode,
		}),
	)

	// The apex. www sends every request to it, keeping the path and query. An
	// exact host wins over a pattern, so www never reads as a workspace named
	// "www".
	r.Host(baseDomain, apexRoutes)
	r.RedirectHost("www."+baseDomain, http.StatusMovedPermanently, baseDomain)

	// One table for every workspace: the subdomain is the {tenant} parameter.
	r.Host(tenantHost, workspaceRoutes(store, session.NewMemoryStore()))

	// Everything else, such as a request sent straight to 127.0.0.1:8080.
	r.Host("*", func(h *router.Router[Ctx]) { h.GET("/", unknownHost) })

	return r
}

// renderError answers a failure with a page, because a browser reads pages.
func renderError(c Ctx, err error) error {
	he := router.HTTPErrorOf(err)
	return c.Render(he.Status, tmpl("error", struct {
		Status  int
		Message string
		ApexURL string
	}{Status: he.Status, Message: he.Message, ApexURL: apexURL(c)}))
}
