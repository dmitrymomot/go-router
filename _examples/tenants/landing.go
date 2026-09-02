package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// apexRoutes answers on the base domain itself: the landing page and the form
// that creates a workspace.
func apexRoutes(h *router.Router[Ctx]) {
	h.GET("/", landing)
	h.GET("/signup", signupForm)
	h.POST("/signup", signup)
	h.POST("/signout", signout)
}

type landingPage struct {
	Email      string
	Workspaces []workspaceLink
}

type workspaceLink struct {
	Name string
	URL  string
}

func landing(c Ctx) error {
	page := landingPage{Email: c.Email}
	for _, w := range c.Store.OwnedBy(c.Email) {
		page.Workspaces = append(page.Workspaces, workspaceLink{
			Name: w.Name,
			URL:  workspaceURL(c, w.Slug),
		})
	}
	return c.Render(http.StatusOK, tmpl("landing", page))
}

type signupPage struct {
	CSRFToken string
	Name      string
	Email     string
	Error     string
	Preview   string
}

func signupForm(c Ctx) error {
	return c.Render(http.StatusOK, tmpl("signup", signupPage{
		CSRFToken: middleware.CSRFTokenFrom(c),
		Email:     c.Email,
	}))
}

type signupInput struct {
	Name  string `form:"name"`
	Email string `form:"email"`
}

func signup(c Ctx) error {
	in, err := c.Bind[signupInput]()
	if err != nil {
		return err
	}

	name, email := cleanName(in.Name), cleanEmail(in.Email)
	page := signupPage{
		CSRFToken: middleware.CSRFTokenFrom(c),
		Name:      name,
		Email:     email,
		Preview:   slugify(name),
	}

	if reason := checkSignup(name, email); reason != "" {
		page.Error = reason
		return c.Render(http.StatusUnprocessableEntity, tmpl("signup", page))
	}

	w, err := c.Store.Create(name, email)
	if err != nil {
		switch {
		case errors.Is(err, ErrSlugEmpty), errors.Is(err, ErrSlugReserved), errors.Is(err, ErrSlugTaken):
			page.Error = "Pick another name: " + err.Error() + "."
			return c.Render(http.StatusUnprocessableEntity, tmpl("signup", page))
		default:
			return err
		}
	}

	writeSession(c, email)

	// The new workspace lives on its own host, so this leaves the apex.
	return c.Redirect(http.StatusSeeOther, workspaceURL(c, w.Slug))
}

func checkSignup(name, email string) string {
	switch {
	case name == "":
		return "Type a name for the workspace."
	case !strings.Contains(email, "@"):
		return "Type the email address that owns it."
	}
	return ""
}

func signout(c Ctx) error {
	clearSession(c)
	return c.Redirect(http.StatusSeeOther, apexURL(c))
}
