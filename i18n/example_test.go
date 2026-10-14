package i18n_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing/fstest"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/i18n"
	"github.com/dmitrymomot/go-router/i18n/cldr"
)

func Example() {
	// Catalogs declare the locales. "vi" works with no change to the package.
	catalogs := fstest.MapFS{
		"en/app.json": &fstest.MapFile{Data: []byte(`{
			"greeting": "Hello, {{name}}!",
			"items": {"zero": "No items", "one": "1 item", "other": "{{count}} items"}
		}`)},
		"vi/app.json": &fstest.MapFile{Data: []byte(`{"greeting": "Xin chào, {{name}}!"}`)},
	}

	bundle, err := i18n.New("en", i18n.WithMessages(catalogs))
	if err != nil {
		panic(err)
	}

	vi := bundle.For(bundle.ParseOrDefault("vi"))
	fmt.Println(vi.T("app.greeting", "name", "Linh"))

	// A key vi lacks falls through to the default locale.
	fmt.Println(vi.TN("app.items", 3))

	// The built-in zero-one-many rule, with no rule wired.
	en := bundle.Default()
	fmt.Println(bundle.TN(en, "app.items", 0))
	fmt.Println(bundle.TN(en, "app.items", 1))

	// Output:
	// Xin chào, Linh!
	// 3 items
	// No items
	// 1 item
}

// Context is the application's own type. It holds the locale a signed-in user
// saved, which FromFunc reads with no lookup.
type Context struct {
	router.Base
	UserLocale string
}

func ExampleMiddlewareWithConfig() {
	bundle, err := i18n.New("en", i18n.WithMessages(fstest.MapFS{
		"en/app.json": &fstest.MapFile{Data: []byte(`{"title": "Orders", "count": {"one": "{{count}} order", "other": "{{count}} orders"}}`)},
		"uk/app.json": &fstest.MapFile{Data: []byte(`{"title": "Замовлення"}`)},
	}), i18n.WithPlural("uk", cldr.Uk))
	if err != nil {
		panic(err)
	}

	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.Use(i18n.MiddlewareWithConfig(i18n.Config[*Context]{
		Bundle: bundle,
		Sources: []i18n.Source{
			i18n.FromFunc("", func(c *Context) (string, bool) { return c.UserLocale, c.UserLocale != "" }),
			i18n.FromCookie("lang"),
			i18n.FromAcceptLanguage(),
		},
	}))
	r.GET("/orders", func(c *Context) error {
		return c.String(http.StatusOK, i18n.T(c, "app.title")+": "+i18n.TN(c, "app.count", 2))
	})

	req := httptest.NewRequest(http.MethodGet, "/orders", nil)
	req.Header.Set("Accept-Language", "uk-UA, en;q=0.8")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	fmt.Println(rec.Body.String())
	fmt.Println(rec.Header().Get("Content-Language"), rec.Header().Values("Vary"))

	// Output:
	// Замовлення: 2 orders
	// uk [Cookie Accept-Language]
}

func ExampleRedirectToLocale() {
	bundle, err := i18n.New("en", i18n.WithMessages(fstest.MapFS{
		"en/app.json": &fstest.MapFile{Data: []byte(`{"hi": "Hello"}`)},
		"uk/app.json": &fstest.MapFile{Data: []byte(`{"hi": "Привіт"}`)},
	}))
	if err != nil {
		panic(err)
	}

	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.ParamClass("locale", bundle.Has) // /fr and /en-gb are a 404
	r.Use(i18n.MiddlewareWithConfig(i18n.Config[*Context]{
		Bundle:  bundle,
		Sources: []i18n.Source{i18n.FromParam("lang"), i18n.FromAcceptLanguage()},
	}))
	r.GET("/", i18n.RedirectToLocale[*Context](bundle, "/{lang}", "lang"))
	r.Route("/{lang:locale}", func(g *router.Router[*Context]) {
		g.GET("/", func(c *Context) error { return c.String(http.StatusOK, i18n.T(c, "app.hi")) })
	})

	for _, path := range []string{"/", "/uk", "/fr"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept-Language", "uk")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		fmt.Println(path, rec.Code, rec.Header().Get("Location")+strings.TrimSpace(rec.Body.String()))
	}

	// Output:
	// / 302 /uk
	// /uk 200 Привіт
	// /fr 404 Not Found
}
