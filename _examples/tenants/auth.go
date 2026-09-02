package main

import (
	"errors"
	"net/http"
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
		Email:     c.Email,
	}))
}

// signup makes the account and its first workspace in one step, then signs the
// new owner in and sends them to the subdomain that is now theirs.
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

	if err := c.Store.Register(email, in.Password); err != nil {
		if errors.Is(err, ErrEmailTaken) {
			return refuse(c, "signup", page, "That email already has an account. Sign in instead.")
		}
		return err
	}

	w, err := c.Store.Create(name, email)
	if err != nil {
		if reason, ok := nameRefused(err); ok {
			return refuse(c, "signup", page, reason)
		}
		return err
	}

	writeSession(c, email)

	// The new workspace lives on its own host, so this leaves the apex.
	return c.Redirect(http.StatusSeeOther, workspaceURL(c, w.Slug))
}

// loginForm is a page of the workspace, not of the apex: each workspace has
// its own door, at its own address.
func loginForm(c Ctx) error {
	return c.Render(http.StatusOK, tmpl("login", credentialsPage{
		CSRFToken: middleware.CSRFTokenFrom(c),
		Email:     c.Email,
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

	// One sentence for three failures: an unknown address, a wrong password,
	// and an account that owns some other workspace. The form gives away
	// neither who has an account nor who owns what.
	err = c.Store.Authenticate(email, in.Password)
	if err != nil || email != c.Workspace.Owner {
		return refuse(c, "login", page, "That email and password do not match.")
	}

	writeSession(c, email)

	return c.Redirect(http.StatusSeeOther, "/")
}

// refuse renders the form again, with the reason and a 422 rather than the
// 200 that would tell a client the request went through.
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
