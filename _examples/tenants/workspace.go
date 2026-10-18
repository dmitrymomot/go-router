package main

import (
	"context"
	"net/http"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
	"github.com/dmitrymomot/go-router/session"
	"github.com/dmitrymomot/go-router/tenant"
	"github.com/dmitrymomot/go-router/user"
)

const sessionLifetime = 12 * time.Hour

// sessionData is what a session holds: the account, and the workspace it
// belongs to, so a session of one workspace opens no other.
type sessionData struct {
	Workspace string `json:"ws"`
	Email     string `json:"email"`
}

// workspaceRoutes answers on every workspace subdomain. One route table serves
// them all, because the subdomain is a route parameter.
//
// Three middlewares run in turn. tenant turns the {tenant} label of the host
// into the workspace, or answers 404. session loads the session that the
// cookie names. user turns the session into the account, and lets a guest
// through: only the dashboard and the sign-out care.
func workspaceRoutes(store *Store, sessions session.Store) func(h *router.Router[Ctx]) {
	findWorkspace := func(_ context.Context, slug string) (Workspace, error) {
		if w, ok := store.Get(slug); ok {
			return w, nil
		}
		return Workspace{}, tenant.ErrUnknown
	}

	// The session names a workspace and an email. The account counts only on
	// the host of that workspace, and only while the workspace holds it.
	findAccount := func(ctx context.Context, email string) (Account, error) {
		d, _ := session.From[sessionData](ctx)
		if w := workspaceOf(ctx); d.Workspace == w.Slug {
			if a, ok := store.Account(w.Slug, email); ok {
				return a, nil
			}
		}
		return Account{}, user.ErrUnknown
	}

	return func(h *router.Router[Ctx]) {
		h.Use(
			tenant.MiddlewareWithConfig(tenant.Config[Ctx, Workspace]{
				Lookup: findWorkspace,
				OnReject: func(c Ctx, _ error) error {
					return router.ErrNotFound.WithMessage("no workspace at %s", c.Host())
				},
			}),
			// The cookie carries no Domain, so it belongs to the host that set
			// it: signing in at acme.lvh.me leaves beta.lvh.me signed out. Over
			// HTTPS its name takes the __Host- prefix, so no other host can set
			// one that stands in front of it.
			session.MiddlewareWithConfig(session.Config[Ctx, sessionData]{
				Store:    sessions,
				Lifetime: sessionLifetime,
			}),
			user.Middleware[Ctx](findAccount, user.FromSession(func(d sessionData) string { return d.Email })),
		)

		h.GET("/", dashboard)
		h.GET(enterPath, enter)
		h.GET("/login", loginForm)
		h.POST("/login", login)

		// A guest has nothing to sign out of, so the door is where they go.
		h.With(user.RequireWithConfig[Ctx, Account](user.RequireConfig{LoginURL: "/login"})).
			POST("/signout", signout)
	}
}

// workspaceOf reports the workspace of the host. Every route of
// workspaceRoutes has one.
func workspaceOf(ctx context.Context) Workspace {
	w, _ := tenant.From[Workspace](ctx)
	return w
}

type dashboardPage struct {
	Workspace Workspace
	Email     string
	ApexURL   string
	CSRFToken string
}

func dashboard(c Ctx) error {
	a, _ := user.From[Account](c)
	return c.Render(http.StatusOK, tmpl("dashboard", dashboardPage{
		Workspace: workspaceOf(c),
		Email:     a.Email,
		ApexURL:   apexURL(c),
		// The sign-out form posts, so it carries a token like any other.
		CSRFToken: middleware.CSRFTokenFrom(c),
	}))
}

// unknownHost answers a request that named neither the apex nor a workspace,
// such as one sent straight to 127.0.0.1.
func unknownHost(c Ctx) error {
	return c.Render(http.StatusNotFound, tmpl("unknown", struct{ Host, Base string }{
		Host: c.Host(),
		Base: baseDomain,
	}))
}
