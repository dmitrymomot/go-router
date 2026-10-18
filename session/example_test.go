package session_test

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/cookie"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/session"
)

func Example() {
	// The store is yours: a table of sessions behind the three methods of
	// Store. This one is the fake of the tests.
	store := newFakeStore()

	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.Use(session.Middleware[*appContext, Data](store))
	r.POST("/login", func(c *appContext) error {
		// ... check the password ...
		// A new token at login, so one planted before it is worth nothing.
		if err := session.Renew(c); err != nil {
			return err
		}
		if err := session.Put(c, Data{UserID: "ada"}); err != nil {
			return err
		}
		return c.Redirect(http.StatusSeeOther, "/")
	})
	r.GET("/", func(c *appContext) error {
		if d, ok := session.From[Data](c); ok {
			return c.String(http.StatusOK, "hello, "+d.UserID)
		}
		return c.String(http.StatusOK, "hello, visitor")
	})
	r.POST("/logout", func(c *appContext) error {
		if err := session.Destroy(c); err != nil {
			return err
		}
		return c.Redirect(http.StatusSeeOther, "/")
	})

	fmt.Println(routertest.Get(r, "/").String())
	sid := routertest.Cookie(routertest.Do(r, http.MethodPost, "/login").Cookies()[0])
	fmt.Println(routertest.Get(r, "/", sid).String())
	routertest.Do(r, http.MethodPost, "/logout", sid)
	// The store dropped the record, so the same cookie names nothing now.
	fmt.Println(routertest.Get(r, "/", sid).String())
	// Output:
	// hello, visitor
	// hello, ada
	// hello, visitor
}

func ExampleNewCookieStore() {
	// The whole session rides in the cookie, encrypted, and the server keeps
	// nothing. A short Lifetime limits a copy of the cookie, which no logout
	// can revoke.
	codec := cookie.NewEncryptedCodec([]byte(strings.Repeat("k", cookie.KeyLen)))

	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.Use(session.MiddlewareWithConfig(session.Config[*appContext, Data]{
		Store:       session.NewCookieStore(codec),
		Lifetime:    8 * time.Hour,
		IdleTimeout: 30 * time.Minute,
	}))
	routes(r)

	first := routertest.Cookie(routertest.Do(r, http.MethodPost, "/cart?item=tea").Cookies()[0])
	second := routertest.Cookie(routertest.Do(r, http.MethodPost, "/cart?item=jam", first).Cookies()[0])
	fmt.Println(routertest.Get(r, "/show", second).String())
	// Output:
	// |tea,jam
}
