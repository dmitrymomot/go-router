package main

import (
	"context"
	"embed"
	"html/template"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
)

//go:embed templates/*.html
var files embed.FS

var templates = template.Must(template.ParseFS(files, "templates/*.html"))

func tmpl(name string, data any) router.Component {
	return router.ComponentFunc(func(_ context.Context, w io.Writer) error {
		return templates.ExecuteTemplate(w, name, data)
	})
}

const sessionCookie = "tenants_session"

const sessionMaxAge = 12 * time.Hour

// writeSession signs the email into a cookie scoped to the whole base domain,
// so every workspace subdomain reads the same session. A cookie without the
// Domain attribute belongs to the host that set it, and the apex could not
// hand it to acme.lvh.me.
func writeSession(c Ctx, email string) {
	c.SetSignedCookie(c.Codec, &http.Cookie{
		Name:     sessionCookie,
		Value:    email,
		Domain:   baseDomain,
		Path:     "/",
		MaxAge:   int(sessionMaxAge / time.Second),
		Secure:   router.SchemeOf(c.Request()) == "https",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func readSession(c Ctx) (string, bool) {
	raw, err := c.SignedCookie(c.Codec, sessionCookie)
	if err != nil {
		return "", false
	}
	email := cleanEmail(string(raw))
	return email, email != ""
}

func clearSession(c Ctx) {
	c.SetCookie(&http.Cookie{
		Name:     sessionCookie,
		Domain:   baseDomain,
		Path:     "/",
		MaxAge:   -1,
		Secure:   router.SchemeOf(c.Request()) == "https",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// workspaceURL is the absolute address of a workspace. The port comes from the
// request that asked, so one binary serves lvh.me:8080 here and lvh.me there.
func workspaceURL(c Ctx, slug string) string {
	host := slug + "." + baseDomain
	if _, port, err := net.SplitHostPort(c.Request().Host); err == nil {
		host = net.JoinHostPort(host, port)
	}
	return c.Scheme() + "://" + host + "/"
}

// apexURL is the address of the landing page, from wherever the request came.
func apexURL(c Ctx) string {
	host := baseDomain
	if _, port, err := net.SplitHostPort(c.Request().Host); err == nil {
		host = net.JoinHostPort(host, port)
	}
	return c.Scheme() + "://" + host + "/"
}

const (
	maxNameRunes  = 40
	maxEmailRunes = 120
)

func cleanName(s string) string { return clean(s, maxNameRunes) }

func cleanEmail(s string) string { return strings.ToLower(clean(s, maxEmailRunes)) }

func clean(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if runes := []rune(s); len(runes) > limit {
		s = strings.TrimSpace(string(runes[:limit]))
	}
	return s
}
