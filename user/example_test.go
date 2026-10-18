package user_test

import (
	"context"
	"fmt"
	"net/http"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/user"
)

func Example() {
	// The Lookup is yours: one query against the accounts table, which also
	// turns away an account that is suspended.
	find := func(_ context.Context, id string) (Account, error) {
		if a, ok := accounts[id]; ok {
			return a, nil
		}
		return Account{}, user.ErrUnknown
	}

	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	// Every page knows its user, and a visitor still sees the public ones.
	r.Use(user.Middleware[*appContext](find, user.FromCookie(codec, "uid")))
	r.GET("/", func(c *appContext) error {
		if a, ok := user.From[Account](c); ok {
			return c.String(http.StatusOK, "hello, "+a.Name)
		}
		return c.String(http.StatusOK, "hello, visitor")
	})
	r.Route("/settings", func(s *router.Router[*appContext]) {
		s.Use(user.RequireWithConfig[*appContext, Account](user.RequireConfig{LoginURL: "/login"}))
		s.GET("/", show)
	})

	fmt.Println(routertest.Get(r, "/", signed("uid", "u1")).String())
	fmt.Println(routertest.Get(r, "/").String())
	fmt.Println(routertest.Get(r, "/settings").Header.Get("Location"))
	// Output:
	// hello, Ada
	// hello, visitor
	// /login?next=%2Fsettings
}

func ExampleMiddlewareWithConfig() {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.Use(user.MiddlewareWithConfig(user.Config[*appContext, Account]{
		Lookup: lookup,
		// The user of a checked API key wins over a Bearer token.
		Sources: []user.Source{
			user.FromFunc("", func(c *appContext) (string, bool) { return c.KeyUser, c.KeyUser != "" }),
			user.FromBearer(),
		},
	}))
	// The API needs a user on every route.
	r.Use(user.Require[*appContext, Account]())
	r.GET("/me", show)

	fmt.Println(routertest.Get(r, "/me").StatusCode)
	fmt.Println(routertest.Get(r, "/me", routertest.Header("Authorization", "Bearer u2")).String())
	// Output:
	// 401
	// Grace
}
