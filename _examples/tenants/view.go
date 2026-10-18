package main

import (
	"context"
	"embed"
	"html/template"
	"io"
	"net"
	"strings"

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

// origin is the scheme and authority of host. The port comes from the request
// that asked, so one binary serves lvh.me:8080 here and lvh.me there.
func origin(c Ctx, host string) string {
	if _, port, err := net.SplitHostPort(c.Request().Host); err == nil {
		host = net.JoinHostPort(host, port)
	}
	return c.Scheme() + "://" + host
}

// workspaceHost is the host of a workspace, built from the pattern that routes
// it.
func workspaceHost(slug string) string { return router.MustExpand(tenantHost, "tenant", slug) }

// workspaceURL is the absolute address of a workspace.
func workspaceURL(c Ctx, slug string) string { return origin(c, workspaceHost(slug)) + "/" }

func apexURL(c Ctx) string { return origin(c, baseDomain) + "/" }

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
