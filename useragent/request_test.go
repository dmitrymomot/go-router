package useragent_test

import (
	"net/http"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/useragent"
)

type appContext struct{ router.Base }

func TestOfParsesOncePerRequest(t *testing.T) {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.Use(func(next router.HandlerFunc[*appContext]) router.HandlerFunc[*appContext] {
		return func(c *appContext) error {
			if useragent.Of(c).IsBot() {
				return router.ErrForbidden
			}
			// A later change to the header no longer counts: the parse is kept.
			c.Request().Header.Set("User-Agent", "curl/8.7.1")
			return next(c)
		}
	})
	r.GET("/", func(c *appContext) error { return c.String(http.StatusOK, useragent.Of(c).String()) })

	routertest.Get(r, "/", routertest.Header("User-Agent", frozenChromeWin),
		routertest.Header("Sec-CH-UA", `"Brave";v="138", "Chromium";v="138"`)).
		Expect(t).Status(http.StatusOK).Body("Brave 138 on Windows 10 (Desktop)")
	routertest.Get(r, "/", routertest.Header("User-Agent", "Googlebot/2.1 (+http://www.google.com/bot.html)")).
		Expect(t).Status(http.StatusForbidden)
}

func TestOfReparsesAValueThatIsNotAParse(t *testing.T) {
	c, _ := routertest.NewContext(t, func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) },
		routertest.WithTarget(http.MethodGet, "/", routertest.Header("User-Agent", frozenChromeWin)))
	c.Set("useragent.parsed", "not a parse")
	equal(t, "Chrome", useragent.Of(c).Browser.Name)
}
