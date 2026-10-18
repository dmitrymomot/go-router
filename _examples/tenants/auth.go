package main

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
	"github.com/dmitrymomot/go-router/session"
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
	w := workspaceOf(c)
	t, ok := c.Store.Redeem(c.Query("ticket"))
	if !ok || t.workspace != w.Slug {
		return c.Redirect(http.StatusSeeOther, "/login")
	}
	if err := signIn(c, w, t.email); err != nil {
		return err
	}
	return c.Redirect(http.StatusSeeOther, "/")
}

// signIn starts the session of email in the workspace w: a new token, a new
// row under its hash, and the cookie. A session the browser already held ends,
// so a token planted before the sign-in names nothing after it.
func signIn(c Ctx, w Workspace, email string) error {
	if old, ok := c.Auth.Sessions.From(c); ok {
		c.Auth.Table.Delete(old.Hash)
	}
	tok := session.NewToken()
	row := c.Auth.Table.Create(w.Slug, email, tok.Hash)
	return c.Auth.Sessions.Start(c, tok, row, row.Expires)
}

func loginForm(c Ctx) error {
	return c.Render(http.StatusOK, tmpl("login", credentialsPage{
		CSRFToken: middleware.CSRFTokenFrom(c),
		Workspace: workspaceOf(c).Name,
	}))
}

func login(c Ctx) error {
	in, err := c.Bind[credentialsInput]()
	if err != nil {
		return err
	}

	w := workspaceOf(c)
	email := cleanEmail(in.Email)
	page := credentialsPage{
		CSRFToken: middleware.CSRFTokenFrom(c),
		Email:     email,
		Workspace: w.Name,
	}

	// One sentence for both failures, so the form says nothing about which
	// addresses hold an account in this workspace.
	if err := c.Store.Authenticate(w.Slug, email, in.Password); err != nil {
		return refuse(c, "login", page, "That email and password do not match.")
	}

	if err := signIn(c, w, email); err != nil {
		return err
	}

	return c.Redirect(http.StatusSeeOther, "/")
}

// signout deletes the row of the session, so a copy of the cookie names
// nothing either, and clears the cookie.
func signout(c Ctx) error {
	if s, ok := c.Auth.Sessions.From(c); ok {
		c.Auth.Table.Delete(s.Hash)
	}
	if err := c.Auth.Sessions.End(c); err != nil {
		return err
	}
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

// enterPath is where a workspace spends a ticket. The route table registers it,
// and enterURL fills it in.
const enterPath = "/enter"

func enterURL(c Ctx, slug, ticket string) string {
	return origin(c, workspaceHost(slug)) + router.MustExpand(enterPath+"?ticket={ticket}", "ticket", ticket)
}
