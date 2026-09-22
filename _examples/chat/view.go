package main

import (
	"context"
	"embed"
	"html/template"
	"io"
	"net/url"
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

func readUser(c Ctx) (string, bool) {
	name, err := url.QueryUnescape(c.Cookie(cookieName))
	if err != nil {
		return "", false
	}
	name = cleanName(name)
	return name, name != ""
}

func writeUser(c Ctx, name string) {
	c.SetCookie(c.NewCookie(cookieName, url.QueryEscape(name), 12*time.Hour))
}

func clearUser(c Ctx) {
	c.ClearCookie(cookieName)
}
