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

// Auth is the sign-in of the workspace hosts: the sessions table, the cookie
// that names a row of it, and the account behind that row.
type Auth struct {
	Table    *SessionTable
	Sessions *session.Sessions[Ctx, Session]
	Users    *user.Users[Ctx, Account]
}

// newAuth wires package session to the sessions table, and package user to
// the session.
func newAuth(store *Store) *Auth {
	table := NewSessionTable()
	// The cookie carries no Domain, so it belongs to the host that set it:
	// signing in at acme.lvh.me leaves beta.lvh.me signed out. Over HTTPS its
	// name takes the __Host- prefix, so no other host can set one that stands
	// in front of it.
	sessions := session.New[Ctx](session.Config[Session]{Find: table.Find})

	// The session names a workspace and an email. The account counts only on
	// the host of that workspace, and only while the workspace holds it.
	users := user.New(func(c Ctx) (Account, error) {
		s, ok := sessions.From(c)
		if !ok || s.Workspace != workspaceOf(c).Slug {
			return Account{}, user.ErrUnknown
		}
		if a, ok := store.Account(s.Workspace, s.Email); ok {
			return a, nil
		}
		return Account{}, user.ErrUnknown
	})

	return &Auth{Table: table, Sessions: sessions, Users: users}
}

// workspaceRoutes answers on every workspace subdomain. One route table serves
// them all, because the subdomain is a route parameter.
//
// Three middlewares run in turn. tenant turns the {tenant} label of the host
// into the workspace, or answers 404. session finds the row that the cookie
// names. user turns the row into the account, and lets a guest through: only
// the dashboard and the sign-out care.
func workspaceRoutes(store *Store, auth *Auth) func(h *router.Router[Ctx]) {
	findWorkspace := func(_ context.Context, slug string) (Workspace, error) {
		if w, ok := store.Get(slug); ok {
			return w, nil
		}
		return Workspace{}, tenant.ErrUnknown
	}

	return func(h *router.Router[Ctx]) {
		h.Use(
			tenant.MiddlewareWithConfig(tenant.Config[Ctx, Workspace]{
				Lookup: findWorkspace,
				OnReject: func(c Ctx, _ error) error {
					return router.ErrNotFound.WithMessage("no workspace at %s", c.Host())
				},
			}),
			auth.Sessions.Middleware(),
			auth.Users.Middleware(),
		)

		h.GET("/", dashboard)
		h.GET(enterPath, enter)
		h.GET("/login", loginForm)
		h.POST("/login", login)

		// A guest has nothing to sign out of, so the door is where they go.
		h.With(auth.Users.Require(user.RequireConfig{LoginURL: "/login"})).POST("/signout", signout)
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
	a, _ := c.Auth.Users.From(c)
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
