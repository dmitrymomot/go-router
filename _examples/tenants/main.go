// Command tenants is a multi-tenant web app on top of go-router: a landing
// page on the base domain, a signup form that creates a workspace, and one
// route table that serves every workspace on its own subdomain.
//
// lvh.me and every name under it resolve to 127.0.0.1, so the whole thing runs
// with no entry in /etc/hosts.
package main

import (
	"context"
	"crypto/rand"
	"errors"
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

// baseDomain is the apex. A workspace named Acme answers on acme.lvh.me.
const baseDomain = "lvh.me"

const addr = "localhost:8080"

const maxBodyBytes = 8 << 10

// Context is what every handler receives. The signed-in email and the
// workspace of the subdomain are filled in by middleware.
type Context struct {
	router.Base
	Store     *Store
	Codec     *router.CookieCodec
	Email     string
	Workspace Workspace
}

type Ctx = *Context

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	r := newRouter(NewStore(), router.NewCookieCodec(sessionKey()))

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

func newRouter(store *Store, codec *router.CookieCodec) *router.Router[Ctx] {
	r := router.New(func(http.ResponseWriter, *http.Request) Ctx {
		return &Context{Store: store, Codec: codec}
	})

	r.ErrorHandler(renderError)
	r.MaxBodyBytes(maxBodyBytes)

	r.Use(
		middleware.Logger[Ctx],
		middleware.Recover[Ctx],
		middleware.Secure[Ctx],
		middleware.BodyLimit[Ctx](maxBodyBytes),
		middleware.CSRFWithConfig[Ctx](middleware.CSRFConfig{
			CookieHTTPOnly: true,
			CookieSameSite: http.SameSiteLaxMode,
		}),
		readSessionInto,
	)

	// The apex, and www beside it. An exact host wins over a pattern, so www
	// never reads as a workspace named "www".
	r.Hosts([]string{baseDomain, "www." + baseDomain}, apexRoutes)

	// One table for every workspace: the subdomain is the {tenant} parameter.
	r.Host("{tenant}."+baseDomain, workspaceRoutes)

	// Everything else, such as a request sent straight to 127.0.0.1:8080.
	r.Host("*", func(h *router.Router[Ctx]) { h.GET("/", unknownHost) })

	return r
}

// readSessionInto puts the signed-in email on the context, and lets an
// anonymous request through: only the workspace pages care.
func readSessionInto(next router.HandlerFunc[Ctx]) router.HandlerFunc[Ctx] {
	return func(c Ctx) error {
		if email, ok := readSession(c); ok {
			c.Email = email
		}
		return next(c)
	}
}

// renderError answers a failure with a page, because a browser reads pages.
func renderError(c Ctx, err error) {
	status := router.StatusOf(err)
	message := http.StatusText(status)
	if he, ok := errors.AsType[*router.HTTPError](err); ok && he.Message != "" {
		message = he.Message
	}
	//nolint:errcheck // The request is already failing; nowhere left to report.
	c.Render(status, tmpl("error", struct {
		Status  int
		Message string
		ApexURL string
	}{Status: status, Message: message, ApexURL: apexURL(c)}))
}

// sessionKey signs the session cookie. A key that lives only for this run ends
// every session with the process, which is right for an example and wrong for
// a service.
func sessionKey() []byte {
	if key := []byte(os.Getenv("SESSION_KEY")); len(key) >= router.MinCookieKeyLen {
		return key
	}
	key := make([]byte, router.MinCookieKeyLen)
	//nolint:errcheck // crypto/rand.Read never fails; it crashes the program.
	rand.Read(key)
	slog.Info("no SESSION_KEY of 32 bytes or more, so this run signs with a new one")
	return key
}
