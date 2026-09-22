package main

import (
	"net/http"
	"strings"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/htmx"
	"github.com/dmitrymomot/go-router/middleware"
)

func index(c Ctx) error {
	if _, ok := readUser(c); ok {
		return c.Redirect(http.StatusSeeOther, "/room")
	}
	return c.Render(http.StatusOK, tmpl("index", joinForm{
		CSRFToken: middleware.CSRFTokenFrom(c),
	}))
}

type joinInput struct {
	Name string `form:"name"`
}

type joinForm struct {
	CSRFToken string
	Name      string
	Error     string
}

func join(c Ctx) error {
	in, err := c.Bind[joinInput]()
	if err != nil {
		return err
	}

	name := cleanName(in.Name)
	if name == "" {
		form := joinForm{
			CSRFToken: middleware.CSRFTokenFrom(c),
			Name:      in.Name,
			Error:     "Type a name of 1 to " + maxNameRunesText + " characters.",
		}
		// htmx swaps the form alone. A browser without JavaScript posted
		// the whole page away, so it gets the whole page back.
		return htmx.RenderPartial(c, http.StatusOK, tmpl("join", form), tmpl("index", form))
	}

	writeUser(c, name)

	return htmx.NewResponse(c).Redirect("/room")
}

func leave(c Ctx) error {
	clearUser(c)
	return htmx.NewResponse(c).Redirect("/")
}

func requireUser(next router.HandlerFunc[Ctx]) router.HandlerFunc[Ctx] {
	return func(c Ctx) error {
		name, ok := readUser(c)
		if !ok {
			if strings.Contains(c.Request().Header.Get(router.HeaderAccept), router.MIMETextEventStream) {
				return router.ErrUnauthorized
			}
			return htmx.NewResponse(c).Redirect("/")
		}
		c.User = name
		return next(c)
	}
}
