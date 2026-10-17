package serve_test

import (
	"fmt"
	"net/http"
	"net/netip"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/serve"
)

type appContext struct{ router.Base }

func ExampleJA4() {
	// Behind CloudFront, the edge ends TLS and reports the JA4 of the client
	// in a header. RealIP believes it from the edge alone, which Trust names.
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.Use(middleware.RealIPWithConfig(middleware.RealIPConfig[*appContext]{
		Trust:     middleware.NewTrustSet(middleware.TrustPrefix(netip.MustParsePrefix("130.176.0.0/16"))),
		Headers:   []string{middleware.HeaderCloudFrontViewerAddress},
		JA4Header: middleware.HeaderCloudFrontViewerJA4Fingerprint,
	}))
	r.GET("/", func(c *appContext) error { return c.String(http.StatusOK, "["+serve.JA4(c)+"]") })

	ja4 := routertest.Header(middleware.HeaderCloudFrontViewerJA4Fingerprint, "t13d1516h2_8daaf6152771_e5627efa2ab1")
	fmt.Println(routertest.Get(r, "/", ja4, routertest.RemoteAddr("130.176.9.9:443")).String())
	fmt.Println(routertest.Get(r, "/", ja4, routertest.RemoteAddr("203.0.113.1:443")).String())
	// Output:
	// [t13d1516h2_8daaf6152771_e5627efa2ab1]
	// []
}
