package useragent_test

import (
	"fmt"
	"net/http"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/useragent"
)

func ExampleParse() {
	for _, s := range []string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.2; +https://openai.com/gptbot)",
		"",
	} {
		fmt.Println(useragent.Parse(s))
	}
	// Output:
	// Mobile Safari 17 on iOS 17 (Mobile)
	// GPTBot (AI crawler)
	// Unknown
}

func ExampleOf() {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.GET("/", func(c *appContext) error {
		// The first call parses; any later one in the request reads it back.
		ua := useragent.Of(c)
		return c.String(http.StatusOK, ua.String())
	})

	// The frozen string says Chrome on Windows 10; the hints say Brave on 11.
	res := routertest.Get(r, "/",
		routertest.Header("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36"),
		routertest.Header("Sec-CH-UA", `"Not)A;Brand";v="8", "Chromium";v="138", "Brave";v="138"`),
		routertest.Header("Sec-CH-UA-Platform", `"Windows"`),
		routertest.Header("Sec-CH-UA-Platform-Version", `"15.0.0"`),
	)
	fmt.Println(res.String())
	// Output:
	// Brave 138 on Windows 11 (Desktop)
}

func ExampleAcceptCH() {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	// From the second request on, Chromium sends the real versions and the
	// model, which useragent.Of reads.
	r.Use(useragent.AcceptCH[*appContext])
	r.GET("/", func(c *appContext) error { return c.String(http.StatusOK, useragent.Of(c).String()) })

	res := routertest.Get(r, "/")
	fmt.Println(res.Header.Get("Accept-CH"))
	// Output:
	// Sec-CH-UA-Platform-Version, Sec-CH-UA-Full-Version-List, Sec-CH-UA-Model
}
