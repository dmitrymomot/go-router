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

func loginForm(c Ctx) error {
	return c.Render(http.StatusOK, tmpl("login", credentialsPage{
		CSRFToken: middleware.CSRFTokenFrom(c),
		Email:     c.Email,
	}))
}

// login sends an owner to their first workspace, and anybody else back to the
// landing page, which lists none.
func login(c Ctx) error {
	in, err := c.Bind[credentialsInput]()
	if err != nil {
		return err
	}

	email := cleanEmail(in.Email)
	page := credentialsPage{
		CSRFToken: middleware.CSRFTokenFrom(c),
		Email:     email,
	}

	if err := c.Store.Authenticate(email, in.Password); err != nil {
		// The message is the same for an unknown address and a wrong
		// password, so the form says nothing about who has an account.
		return refuse(c, "login", page, "That email and password do not match.")
	}

	writeSession(c, email)

	if owned := c.Store.OwnedBy(email); len(owned) > 0 {
		return c.Redirect(http.StatusSeeOther, workspaceURL(c, owned[0].Slug))
	}
	return c.Redirect(http.StatusSeeOther, apexURL(c))
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
