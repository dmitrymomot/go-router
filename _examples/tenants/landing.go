package main

import (
	"errors"
	"net/http"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// apexRoutes answers on the base domain itself: the landing page, the signup
// form and the one that adds another workspace. There is no login here. An
// owner signs in at the address of their workspace, and workspaceRoutes
// answers that.
func apexRoutes(h *router.Router[Ctx]) {
	h.GET("/", landing)
	h.GET("/signup", signupForm)
	h.POST("/signup", signup)
	h.POST("/signout", signout)

	// One more middleware, and the same tree. Only this route needs a session.
	h.With(requireSession).POST("/workspaces", newWorkspace)
}

type landingPage struct {
	CSRFToken  string
	Email      string
	Error      string
	Workspaces []workspaceLink
}

type workspaceLink struct {
	Name string
	URL  string
}

func landing(c Ctx) error {
	return c.Render(http.StatusOK, landingFor(c, ""))
}

func landingFor(c Ctx, reason string) router.Component {
	page := landingPage{
		CSRFToken: middleware.CSRFTokenFrom(c),
		Email:     c.Email,
		Error:     reason,
	}
	for _, w := range c.Store.OwnedBy(c.Email) {
		page.Workspaces = append(page.Workspaces, workspaceLink{
			Name: w.Name,
			URL:  workspaceURL(c, w.Slug),
		})
	}
	return tmpl("landing", page)
}

type workspaceInput struct {
	Name string `form:"name"`
}

// newWorkspace adds a workspace to the account that is already signed in, so
// it asks for a name and nothing else.
func newWorkspace(c Ctx) error {
	in, err := c.Bind[workspaceInput]()
	if err != nil {
		return err
	}

	w, err := c.Store.Create(cleanName(in.Name), c.Email)
	if err != nil {
		if reason, ok := nameRefused(err); ok {
			return c.Render(http.StatusUnprocessableEntity, landingFor(c, reason))
		}
		return err
	}
	return c.Redirect(http.StatusSeeOther, workspaceURL(c, w.Slug))
}

// nameRefused turns a slug rule into a sentence for the reader, and says
// whether the error was one of those rules at all.
func nameRefused(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrSlugEmpty), errors.Is(err, ErrSlugReserved), errors.Is(err, ErrSlugTaken):
		return "Pick another name: " + err.Error() + ".", true
	default:
		return "", false
	}
}

// requireSession sends an anonymous reader back to the landing page, which
// says where the doors are: signup here, login at a workspace.
func requireSession(next router.HandlerFunc[Ctx]) router.HandlerFunc[Ctx] {
	return func(c Ctx) error {
		if c.Email == "" {
			return c.Redirect(http.StatusSeeOther, "/")
		}
		return next(c)
	}
}

func signout(c Ctx) error {
	clearSession(c)
	return c.Redirect(http.StatusSeeOther, apexURL(c))
}
