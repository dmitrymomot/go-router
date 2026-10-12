package profiler_test

import (
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
	"github.com/dmitrymomot/go-router/middleware/profiler"
	"github.com/dmitrymomot/go-router/routertest"
)

type Context struct{ router.Base }

func ExampleHandler() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	// The profiler exposes the internals of the process: guard it.
	r.Route("/debug", func(g *router.Router[*Context]) {
		g.Use(middleware.BasicAuth(func(_ *Context, user, pass string) (bool, error) {
			return middleware.SecureCompare(user, "ops") && middleware.SecureCompare(pass, "s3cret"), nil
		}))
		g.MountHandler("/", profiler.Handler())
	})

	creds := base64.StdEncoding.EncodeToString([]byte("ops:s3cret"))
	anon := routertest.Get(r, "/debug/vars")
	authed := routertest.Get(r, "/debug/vars", routertest.Header("Authorization", "Basic "+creds))
	fmt.Println(anon.StatusCode, authed.StatusCode)
	// Output: 401 200
}
