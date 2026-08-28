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

var dist = fstest.MapFS{
	"index.html":  {Data: []byte("<h1>app</h1>")},
	"css/app.css": {Data: []byte("body{}")},
}

func Example() {
	assets := static.Must(static.Config{FS: dist, Prefix: "/static", Build: "v1"})

	r := router.New(func(http.ResponseWriter, *http.Request) *appContext {
		return new(appContext)
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
