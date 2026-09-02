package main

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/dmitrymomot/go-router/middleware"
)

const minPasswordRunes = 8

type credentialsPage struct {
	CSRFToken string
	Name      string
	Email     string
	Error     string
	Workspace string
}

type credentialsInput struct {
	Name     string `form:"name"`
	Email    string `form:"email"`
	Password string `form:"password"`
}

func signupForm(c Ctx) error {
	return c.Render(http.StatusOK, tmpl("signup", credentialsPage{
		CSRFToken: middleware.CSRFTokenFrom(c),
	}))
}

// signup makes the workspace and its first account, then hands the new owner
// to the workspace host with a ticket, because the apex cannot set a cookie
// for a host below it.
func signup(c Ctx) error {
	in, err := c.Bind[credentialsInput]()
	if err != nil {
		return err
	}

	name, email := cleanName(in.Name), cleanEmail(in.Email)
	page := credentialsPage{
		CSRFToken: middleware.CSRFTokenFrom(c),
		Name:      name,
		Email:     email,
	}

	if reason := checkSignup(name, email, in.Password); reason != "" {
		return refuse(c, "signup", page, reason)
	}

	w, err := c.Store.Create(name)
	if err != nil {
		if reason, ok := nameRefused(err); ok {
			return refuse(c, "signup", page, reason)
		}
		return err
	}

	// The workspace is new, so it holds no account this could collide with.
	if err := c.Store.Register(w.Slug, email, in.Password); err != nil {
		return err
	}

	return c.Redirect(http.StatusSeeOther, enterURL(c, w.Slug, c.Store.NewTicket(w.Slug, email)))
}

func nameRefused(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrSlugEmpty), errors.Is(err, ErrSlugReserved), errors.Is(err, ErrSlugTaken):
		return "Pick another name: " + err.Error() + ".", true
	default:
		return "", false
	}
}

// enter spends the ticket signup issued, on the host the session belongs to.
func enter(c Ctx) error {
	t, ok := c.Store.Redeem(c.Query("ticket"))
	if !ok || t.workspace != c.Workspace.Slug {
		return c.Redirect(http.StatusSeeOther, "/login")
	}
	writeSession(c, t.email)
	return c.Redirect(http.StatusSeeOther, "/")
}

func loginForm(c Ctx) error {
	return c.Render(http.StatusOK, tmpl("login", credentialsPage{
		CSRFToken: middleware.CSRFTokenFrom(c),
		Workspace: c.Workspace.Name,
	}))
}

func login(c Ctx) error {
	in, err := c.Bind[credentialsInput]()
	if err != nil {
		return err
	}

	email := cleanEmail(in.Email)
	page := credentialsPage{
		CSRFToken: middleware.CSRFTokenFrom(c),
		Email:     email,
		Workspace: c.Workspace.Name,
	}

	// One sentence for both failures, so the form says nothing about which
	// addresses hold an account in this workspace.
	if err := c.Store.Authenticate(c.Workspace.Slug, email, in.Password); err != nil {
		return refuse(c, "login", page, "That email and password do not match.")
	}

	writeSession(c, email)

	return c.Redirect(http.StatusSeeOther, "/")
}

func signout(c Ctx) error {
	clearSession(c)
	return c.Redirect(http.StatusSeeOther, "/login")
}

// refuse renders the form again with a 422, not the 200 that would tell a
// client the request went through.
func refuse(c Ctx, name string, page credentialsPage, reason string) error {
	page.Error = reason
	return c.Render(http.StatusUnprocessableEntity, tmpl(name, page))
}

func checkSignup(name, email, password string) string {
	switch {
	case name == "":
		return "Type a name for the workspace."
	case !strings.Contains(email, "@"):
		return "Type the email address that owns it."
	case utf8.RuneCountInString(password) < minPasswordRunes:
		return "The password needs at least 8 characters."
	}
	return ""
}

func enterURL(c Ctx, slug, ticket string) string {
	return workspaceURL(c, slug) + "enter?ticket=" + url.QueryEscape(ticket)
}
