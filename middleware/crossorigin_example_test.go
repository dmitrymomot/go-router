package middleware_test

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
	"github.com/dmitrymomot/go-router/routertest"
)

// CrossOrigin and CSRF guard the same door. CrossOrigin needs no token and no
// cookie, and trusts the Sec-Fetch-Site header of the browser; CSRF hands out a
// token that every form must send back, and so it also covers a browser that
// sends no such header.
func ExampleCrossOrigin() {
	r := newAPI()
	r.Use(middleware.CrossOriginWithConfig(middleware.CrossOriginConfig[*Context]{
		TrustedOrigins: []string{"https://partner.example"},
	}))
	r.POST("/save", sayOK)

	post := func(site, origin string) int {
		opts := []routertest.RequestOption{routertest.Header(router.HeaderSecFetchSite, site)}
		if origin != "" {
			opts = append(opts, routertest.Header(router.HeaderOrigin, origin))
		}
		return routertest.Do(r, http.MethodPost, "/save", opts...).StatusCode
	}

	fmt.Println("same origin:", post("same-origin", ""))
	fmt.Println("another site:", post("cross-site", "https://evil.example"))
	fmt.Println("trusted site:", post("cross-site", "https://partner.example"))
	// Output:
	// same origin: 200
	// another site: 403
	// trusted site: 200
}

func ExampleGzipWithConfig() {
	r := newAPI()
	r.Use(middleware.GzipWithConfig(middleware.GzipConfig[*Context]{
		// Replace the default list of types that are compressed already.
		ExcludeContentTypes: []string{"application/pdf", "image/*"},
	}))
	body := strings.Repeat("a", 2000)
	r.GET("/text", func(c *Context) error { return c.String(http.StatusOK, body) })
	r.GET("/doc", func(c *Context) error {
		c.SetHeader(router.HeaderContentType, "application/pdf")
		_, err := c.Response().WriteString(body)
		return err
	})

	// A client that takes deflate and not gzip gets deflate.
	accept := routertest.Header(router.HeaderAcceptEncoding, "deflate, br;q=0.5")
	fmt.Printf("text: %q\n", routertest.Get(r, "/text", accept).Header.Get(router.HeaderContentEncoding))
	fmt.Printf("pdf: %q\n", routertest.Get(r, "/doc", accept).Header.Get(router.HeaderContentEncoding))
	// Output:
	// text: "deflate"
	// pdf: ""
}
