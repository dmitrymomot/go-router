package static_test

import (
	"fmt"
	"html/template"
	"net/http"
	"os"
	"testing/fstest"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/static"
)

// dist stands in for the embedded build output. A real program writes
//
//	//go:embed all:dist
//	var dist embed.FS
//
// and passes Root "dist", because //go:embed keeps the directory name in every
// path.
var dist = fstest.MapFS{
	"index.html":  {Data: []byte("<h1>app</h1>")},
	"css/app.css": {Data: []byte("body{}")},
}

func Example() {
	// Build is normally empty, so that New derives it from the content. This
	// example pins it to keep the output stable.
	assets := static.Must(static.Config{FS: dist, Prefix: "/static", Build: "v1"})

	r := router.New(func(http.ResponseWriter, *http.Request) *Context {
		return new(Context)
	})
	static.Mount(r, assets)

	fmt.Println(assets.URL("css/app.css"))
	res := routertest.Get(r, assets.URL("css/app.css"))
	fmt.Println(res.StatusCode, res.Header.Get("Cache-Control"))
	fmt.Println(routertest.Get(r, "/static/v0/css/app.css").StatusCode)
	// Output:
	// /static/v1/css/app.css
	// 200 public, max-age=31536000, immutable
	// 404
}

func ExampleAssets_FuncMap() {
	assets := static.Must(static.Config{FS: dist, Prefix: "/static", Build: "v1"})

	page := `<link rel="stylesheet" href="{{ asset "css/app.css" }}">`
	tmpl := template.Must(template.New("page").Funcs(assets.FuncMap()).Parse(page))

	//nolint:errcheck // The example writes to stdout.
	tmpl.Execute(os.Stdout, nil)
	// Output:
	// <link rel="stylesheet" href="/static/v1/css/app.css">
}

// Context is the request context of the example application.
type Context struct {
	router.Base
}
