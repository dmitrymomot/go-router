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
