package main

import (
	"net/http"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// apexRoutes answers on the base domain itself: the pitch and the form that
// makes a workspace. There is no session here and no login. An account belongs
// to one workspace, so its door is that workspace's own address, and
// workspaceRoutes answers it.
func apexRoutes(h *router.Router[Ctx]) {
	h.GET("/", landing)
	h.GET("/signup", signupForm)
	h.POST("/signup", signup)
}

type landingPage struct {
	CSRFToken string
	Example   string
}

func landing(c Ctx) error {
	return c.Render(http.StatusOK, tmpl("landing", landingPage{
		CSRFToken: middleware.CSRFTokenFrom(c),
		Example:   workspaceURL(c, "acme"),
	}))
}
