package user_test

import (
	"fmt"
	"net/http"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/user"
)

func Example() {
	// The load function is yours. Here it reads a header; an application
	// reads its session and looks the account up.
	users := user.New(func(c *appContext) (Account, error) {
		if a, ok := accounts[c.Request().Header.Get("X-User")]; ok {
			return a, nil
		}
		return Account{}, user.ErrUnknown
	})

	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	// Every page knows its user, and a visitor still sees the public ones.
	r.Use(users.Middleware())
	r.GET("/", func(c *appContext) error {
		if a, ok := users.From(c); ok {
			return c.String(http.StatusOK, "hello, "+a.Name)
		}
		return c.String(http.StatusOK, "hello, visitor")
	})
	r.With(users.Require(user.RequireConfig{LoginURL: "/login"})).GET("/settings", show)

	fmt.Println(routertest.Get(r, "/", routertest.Header("X-User", "u1")).String())
	fmt.Println(routertest.Get(r, "/").String())
	fmt.Println(routertest.Get(r, "/settings").Header.Get("Location"))
	fmt.Println(routertest.Get(r, "/settings", routertest.Header("Accept", "application/json")).StatusCode)
	// Output:
	// hello, Ada
	// hello, visitor
	// /login?next=%2Fsettings
	// 401
}
