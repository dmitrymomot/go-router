package tenant_test

import (
	"context"
	"fmt"
	"net/http"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/tenant"
)

func Example() {
	// The Lookup is yours: one query against the tenants table, which also
	// turns away a workspace that is suspended.
	find := func(_ context.Context, slug string) (Workspace, error) {
		if w, ok := workspaces[slug]; ok {
			return w, nil
		}
		return Workspace{}, tenant.ErrUnknown
	}

	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.Host("{tenant}.example.com", func(h *router.Router[*appContext]) {
		h.Use(tenant.Middleware[*appContext](find))
		h.GET("/", func(c *appContext) error {
			w, _ := tenant.From[Workspace](c)
			return c.String(http.StatusOK, "welcome to "+w.Name)
		})
	})

	for _, host := range []string{"acme.example.com", "nobody.example.com"} {
		res := routertest.Get(r, "/", routertest.Host(host))
		fmt.Println(res.StatusCode, res.String())
	}
	// Output:
	// 200 welcome to Acme
	// 404 Not Found
}

func ExampleMiddlewareWithConfig() {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.Use(tenant.MiddlewareWithConfig(tenant.Config[*appContext, Workspace]{
		Lookup: lookup,
		// The tenant of a checked API key wins over the header a client sends.
		Sources: []tenant.Source{
			tenant.FromFunc("", func(c *appContext) (string, bool) { return c.KeyTenant, c.KeyTenant != "" }),
			tenant.FromHeader("X-Tenant-ID"),
		},
		// The landing page needs no tenant.
		Optional: true,
	}))
	r.GET("/", show)

	fmt.Println(routertest.Get(r, "/").String())
	fmt.Println(routertest.Get(r, "/", routertest.Header("X-Tenant-ID", "globex")).String())
	// Output:
	// none
	// Globex
}
