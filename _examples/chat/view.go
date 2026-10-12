package main

import (
	"context"
	"embed"
	"html/template"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

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

const (
	maxNameRunes = 24
	maxTextRunes = 500
)

var maxNameRunesText = strconv.Itoa(maxNameRunes)

func cleanName(s string) string { return clean(s, maxNameRunes) }

func cleanText(s string) string { return clean(s, maxTextRunes) }

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

const cookieName = "chat_user"

const userMaxAge = 12 * time.Hour

// readUser reports the name the signed cookie carries. A cookie the codec does
// not verify counts as no cookie, so no client can post under a name it was
// not given.
func readUser(c Ctx) (string, bool) {
	v, err := c.Cookies.Get(c, cookieName)
	if err != nil {
		return "", false
	}
	name := cleanName(v)
	return name, name != ""
}

// writeUser signs name into the cookie. A name is at most 24 runes, far below
// the size of a cookie, so ErrTooLarge cannot happen.
func writeUser(c Ctx, name string) error {
	return c.Cookies.Set(c, c.NewCookie(cookieName, name, userMaxAge))
}

func clearUser(c Ctx) {
	c.ClearCookie(cookieName)
}
