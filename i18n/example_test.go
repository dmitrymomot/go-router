package i18n_test

import (
	"fmt"
	"testing/fstest"

	"github.com/dmitrymomot/go-router/i18n"
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
