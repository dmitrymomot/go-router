package main

import (
	"net/http"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// workspaceRoutes answers on every workspace subdomain. One route table serves
// them all, because the subdomain is a route parameter.
func workspaceRoutes(h *router.Router[Ctx]) {
	h.Use(loadWorkspace)

	h.GET("/", dashboard)
	h.GET(enterPath, enter)
	h.GET("/login", loginForm)
	h.POST("/login", login)
	h.POST("/signout", signout)
}

// loadWorkspace turns the {tenant} label of the host into the workspace, or
// answers 404. Every handler below it can read c.Workspace. It then puts the
// signed-in email on the context, and lets an anonymous request through: only
// the dashboard cares. The session is read here, and not in a global
// middleware, because it is valid only for the workspace of the host.
func loadWorkspace(next router.HandlerFunc[Ctx]) router.HandlerFunc[Ctx] {
	return func(c Ctx) error {
		slug := c.Param("tenant")
		w, ok := c.Store.Get(slug)
		if !ok {
			return router.ErrNotFound.WithMessage("no workspace at %s", c.Host())
		}
		c.Workspace = w
		if email, ok := readSession(c); ok {
			c.Email = email
		}
		return next(c)
	}
}

type dashboardPage struct {
	Workspace Workspace
	Email     string
	ApexURL   string
	CSRFToken string
}

func dashboard(c Ctx) error {
	return c.Render(http.StatusOK, tmpl("dashboard", dashboardPage{
		Workspace: c.Workspace,
		Email:     c.Email,
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
