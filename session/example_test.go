package session_test

import (
	"fmt"
	"net/http"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/session"
)

func Example() {
	// The table is yours: here a map, in an application a sessions table with
	// a token_hash column. Find is one SELECT by that hash.
	tab := newTable()
	sessions := session.New[*appContext](session.Config[Row]{Find: tab.find})

	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.Use(sessions.Middleware())
	r.POST("/login", func(c *appContext) error {
		// ... check the credentials ...
		tok := session.NewToken()
		row := tab.insert("ada", tok.Hash) // INSERT INTO sessions (user_id, token_hash, ...)
		if err := sessions.Start(c, tok, row, time.Now().Add(30*24*time.Hour)); err != nil {
			return err
		}
		return c.Redirect(http.StatusSeeOther, "/")
	})
	r.GET("/", func(c *appContext) error {
		if row, ok := sessions.From(c); ok {
			return c.String(http.StatusOK, "hello, "+row.UserID)
		}
		return c.String(http.StatusOK, "hello, visitor")
	})
	r.POST("/logout", func(c *appContext) error {
		if row, ok := sessions.From(c); ok {
			tab.delete(row.Hash) // DELETE FROM sessions WHERE id = $1
		}
		if err := sessions.End(c); err != nil {
			return err
		}
		return c.Redirect(http.StatusSeeOther, "/")
	})

	fmt.Println(routertest.Get(r, "/").String())
	sid := routertest.Cookie(routertest.Do(r, http.MethodPost, "/login").Cookies()[0])
	fmt.Println(routertest.Get(r, "/", sid).String())
	routertest.Do(r, http.MethodPost, "/logout", sid)
	// The row is gone, so the same cookie names nothing now.
	fmt.Println(routertest.Get(r, "/", sid).String())
	// Output:
	// hello, visitor
	// hello, ada
	// hello, visitor
}
