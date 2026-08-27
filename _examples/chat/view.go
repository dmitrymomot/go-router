package main

import (
	"context"
	"embed"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/dmitrymomot/go-router"
)

//go:embed templates/*.html
var files embed.FS

// templates holds every page and every fragment of this application. It parses
// once, at start, and html/template is safe to execute from several requests
// at the same time.
var templates = template.Must(template.ParseFS(files, "templates/*.html"))

// tmpl returns the named template as a [router.Component], which is what
// Render and SendComponent take. The router declares that interface for the
// templ generator; html/template reaches it through [router.ComponentFunc].
func tmpl(name string, data any) router.Component {
	return router.ComponentFunc(func(_ context.Context, w io.Writer) error {
		return templates.ExecuteTemplate(w, name, data)
	})
}

// The limits that a name and a message obey. html/template escapes what a
// visitor types, so the limits are about the room and not about safety.
const (
	maxNameRunes = 24
	maxTextRunes = 500
)

// maxNameRunesText is the limit as the form shows it.
var maxNameRunesText = strconv.Itoa(maxNameRunes)

// cleanName returns the name to show, or an empty string for one that the room
// cannot use.
func cleanName(s string) string { return clean(s, maxNameRunes) }

// cleanText returns the message to show, or an empty string for one that
// carries nothing.
func cleanText(s string) string { return clean(s, maxTextRunes) }

// clean drops every control character, collapses runs of space, and cuts what
// is left to max runes.
func clean(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if runes := []rune(s); len(runes) > limit {
		s = strings.TrimSpace(string(runes[:limit]))
	}
	return s
}

// cookieName is where the browser keeps the name of the visitor. A real
// application signs a session instead; this one only has to tell two windows
// apart.
const cookieName = "chat_user"

// readUser returns the name that the visitor typed.
func readUser(c Ctx) (string, bool) {
	ck, err := c.Cookie(cookieName)
	if err != nil {
		return "", false
	}
	name, err := url.QueryUnescape(ck.Value)
	if err != nil {
		return "", false
	}
	name = cleanName(name)
	return name, name != ""
}

// writeUser remembers the name for half a day.
func writeUser(c Ctx, name string) {
	c.SetCookie(&http.Cookie{
		Name:     cookieName,
		Value:    url.QueryEscape(name),
		Path:     "/",
		MaxAge:   int(12 * time.Hour / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearUser forgets the name.
func clearUser(c Ctx) {
	c.SetCookie(&http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
