package main

import (
	"net/http"

	"github.com/dmitrymomot/go-router"
)

// workspaceRoutes answers on every workspace subdomain. One route table
// serves them all, because the subdomain is a route parameter.
func workspaceRoutes(h *router.Router[Ctx]) {
	h.Use(loadWorkspace)

	h.GET("/", dashboard)
	h.POST("/signout", signout)
}

// loadWorkspace turns the {tenant} label of the host into the workspace, or
// answers 404. Every handler below it can read c.Workspace.
func loadWorkspace(next router.HandlerFunc[Ctx]) router.HandlerFunc[Ctx] {
	return func(c Ctx) error {
		slug := c.Param("tenant")
		w, ok := c.Store.Get(slug)
		if !ok {
			return router.ErrNotFound.WithMessage("no workspace at %s", c.Host())
		}
		c.Workspace = w
		return next(c)
	}
}

type dashboardPage struct {
	Workspace Workspace
	Email     string
	ApexURL   string
	IsOwner   bool
}

func dashboard(c Ctx) error {
	return c.Render(http.StatusOK, tmpl("dashboard", dashboardPage{
		Workspace: c.Workspace,
		Email:     c.Email,
		ApexURL:   apexURL(c),
		IsOwner:   c.Email != "" && c.Email == c.Workspace.Owner,
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
