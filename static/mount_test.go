package static_test

import (
	"net/http"
	"testing"
	"testing/fstest"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/static"
)

type appContext struct {
	router.Base
}

func newRouter() *router.Router[*appContext] {
	return router.New(func(http.ResponseWriter, *http.Request) *appContext {
		return new(appContext)
	})
}

func TestMountServesTheAssets(t *testing.T) {
	r := newRouter()
	a := newAssets(t, static.Config{FS: assetFS(), Prefix: "/static"})
	static.Mount(r, a)

	res := routertest.Get(r, a.URL("css/app.css"))
	res.AssertStatus(t, http.StatusOK)
	res.AssertBody(t, appCSS)
	res.AssertHeader(t, "Cache-Control", "public, max-age=31536000, immutable")
}

func TestMountAnswersTheBarePrefixWithTheIndex(t *testing.T) {
	r := newRouter()
	static.Mount(r, newAssets(t, static.Config{FS: assetFS(), Prefix: "/static"}))

	routertest.Get(r, "/static").AssertBody(t, rootIndex)
	routertest.Get(r, "/static/").AssertBody(t, rootIndex)
}

func TestMountSendsAMissToTheErrorHandler(t *testing.T) {
	r := newRouter()
	r.ErrorHandler(func(c *appContext, err error) {
		//nolint:errcheck // The recorder never fails.
		c.String(router.StatusOf(err), "handled: "+err.Error())
	})
	static.Mount(r, newAssets(t, static.Config{FS: assetFS(), Prefix: "/static"}))

	res := routertest.Get(r, "/static/absent.css")
	res.AssertStatus(t, http.StatusNotFound)
	res.AssertBody(t, "handled: 404 Not Found")
}

func TestMountRegistersGETAndHEADOnly(t *testing.T) {
	r := newRouter()
	static.Mount(r, newAssets(t, static.Config{FS: assetFS(), Prefix: "/static"}))

	res := routertest.Do(r, http.MethodPost, "/static/css/app.css")
	res.AssertStatus(t, http.StatusMethodNotAllowed)
	res.AssertHeader(t, "Allow", "GET, HEAD, OPTIONS")

	routertest.Do(r, http.MethodHead, "/static/css/app.css").AssertStatus(t, http.StatusOK)
}

func TestMountAtTheRootKeepsTheOtherRoutes(t *testing.T) {
	r := newRouter()
	r.GET("/api/ping", func(c *appContext) error { return c.String(http.StatusOK, "pong") })
	static.Mount(r, newAssets(t, static.Config{FS: assetFS(), SPA: true}))

	routertest.Get(r, "/api/ping").AssertBody(t, "pong")
	routertest.Get(r, "/js/app.js").AssertBody(t, appJS)
	routertest.Get(r, "/").AssertBody(t, rootIndex)
	routertest.Get(r, "/orders/7").AssertBody(t, rootIndex)
}

func TestMountKeepsAnEscapedPathInside(t *testing.T) {
	fsys := assetFS()
	fsys["secret.txt"] = &fstest.MapFile{Data: []byte("secret")}
	r := newRouter()
	static.Mount(r, newAssets(t, static.Config{FS: fsys, Root: "css", Prefix: "/static"}))

	for _, target := range []string{"/static/../secret.txt", "/static/%2e%2e/secret.txt"} {
		if body := routertest.Get(r, target).String(); body == "secret" {
			t.Fatalf("%s escaped the asset root", target)
		}
	}
}

func TestHandlerReadsTheCatchAllParameter(t *testing.T) {
	r := newRouter()
	a := newAssets(t, static.Config{FS: assetFS(), Prefix: "/files"})
	r.GET("/files/{"+static.PathParam+"...}", static.Handler[*appContext](a))

	routertest.Get(r, "/files/js/app.js").AssertBody(t, appJS)
}

func TestMountHandlerServesTheAssetsToo(t *testing.T) {
	r := newRouter()
	a := newAssets(t, static.Config{FS: assetFS(), Prefix: "/static"})
	r.MountHandler(a.Prefix(), a)

	res := routertest.Get(r, "/static/css/app.css")
	res.AssertStatus(t, http.StatusOK)
	res.AssertBody(t, appCSS)
}

func TestHandlerReportsAWrongMethod(t *testing.T) {
	r := newRouter()
	a := newAssets(t, static.Config{FS: assetFS(), Prefix: "/static"})
	r.Any("/static/{"+static.PathParam+"...}", static.Handler[*appContext](a))

	res := routertest.Do(r, http.MethodPost, "/static/css/app.css")
	res.AssertStatus(t, http.StatusMethodNotAllowed)
	res.AssertHeader(t, "Allow", "GET, HEAD")
}

func TestHandlerOnARouteWithoutTheParameter(t *testing.T) {
	r := newRouter()
	a := newAssets(t, static.Config{FS: assetFS(), Prefix: "/static"})
	r.GET("/static/css/app.css", static.Handler[*appContext](a))
	r.GET("/favicon.ico", static.Handler[*appContext](a))

	routertest.Get(r, "/static/css/app.css").AssertBody(t, appCSS)
	routertest.Get(r, "/favicon.ico").AssertStatus(t, http.StatusNotFound)
}

func TestMountInsideAHostScope(t *testing.T) {
	r := newRouter()
	a := newAssets(t, static.Config{FS: assetFS(), Prefix: "/static"})
	r.Host("{tenant}.example.com", func(h *router.Router[*appContext]) {
		static.Mount(h, a)
		h.GET("/who", func(c *appContext) error {
			return c.String(http.StatusOK, c.Param("tenant"))
		})
	})

	res := routertest.Get(r, a.URL("css/app.css"), routertest.Host("acme.example.com"))
	res.AssertStatus(t, http.StatusOK)
	res.AssertBody(t, appCSS)

	routertest.Get(r, "/who", routertest.Host("acme.example.com")).AssertBody(t, "acme")

	routertest.Get(r, a.URL("css/app.css"), routertest.Host("other.invalid")).
		AssertStatus(t, http.StatusNotFound)
}
