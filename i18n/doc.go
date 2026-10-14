// Package i18n provides internationalization as pure mechanism: message
// catalogs, locale negotiation, and locale-aware formatting, with no
// knowledge of any actual language.
//
// The package ships no list of locales, no per-language plural rules, and no
// per-locale number or date formats. The set of supported locales is
// whatever your catalogs declare — ship a vi/ directory and Vietnamese
// works, with no change to this package. A locale with no wired plural rule
// uses DefaultRule (zero/one/many); a locale with no wired format uses
// Invariant (ISO-8601, "." decimal). Neither is an error, and neither
// pretends to know your language's grammar.
//
// # Usage
//
//	catalog := fstest.MapFS{"en/app.json": &fstest.MapFile{Data: []byte(`{"greeting": "Hello, {{name}}!"}`)}}
//	bundle, err := i18n.New("en", i18n.WithMessages(catalog))
//	if err != nil {
//		log.Fatal(err)
//	}
//	fmt.Println(bundle.T(bundle.Default(), "app.greeting", "name", "Ann"))
//	// Output: Hello, Ann!
//
// # CLDR data
//
// Correct CLDR data lives in the sibling package i18n/cldr and is
// never applied automatically:
//
//	bundle, err := i18n.New("uk-UA",
//	    i18n.WithMessages(catalogFS),
//	    i18n.WithPlural("uk", cldr.Uk),
//	    i18n.WithFormat("uk-UA", cldr.FormatUkUA),
//	)
//
// Both resolve tag → base language → built-in default, so a rule wired for
// "uk" covers uk-UA, and one wired for "pt-PT" overrides "pt" there. The
// reverse does not hold: FormatUkUA applies to a uk-UA locale and not to a
// bare uk locale. Wire a plural rule per language, a format per region.
//
// # Catalogs
//
// Catalogs are JSON under an fs.FS, laid out as {tag}/{namespace}.json. The
// namespace becomes the leading key segment and nested objects flatten to
// dot notation, so en/app.json's {"buttons": {"save": "Save"}} is
// app.buttons.save.
//
//	//go:embed locales
//	var localesFS embed.FS
//
//	sub, _ := fs.Sub(localesFS, "locales")
//	bundle, err := i18n.New("en", i18n.WithMessages(sub))
//
// # Lookup and fallback
//
// Resolution is exact tag, then base language, then the default locale:
// en-GB falls back to en, and es-MX to es, then to the default. The same
// chain applies per key, so a partially translated regional catalog layers
// over its base language.
//
// # Plurals
//
// A JSON object whose keys are all CLDR category names is a plural message:
//
//	{"items": {"zero": "Cart is empty", "one": "1 item", "other": "{{count}} items"}}
//
// TN selects the form with the locale's rule and injects the count as
// {{count}}. A missing form falls back along two→few→many→other,
// few→many→other, zero/one/many→other. If the catalog defines a zero form,
// it wins at n == 0 regardless of the rule — a convenience for translators.
//
// The rule is exact: a JSON object whose keys are all category names is always
// a plural, so a namespace that uses only category words as keys ({"one": "One
// size", "other": "Other sizes"}) is parsed as one too. Give such a namespace
// one non-category key to keep it a namespace; declaring rendered keys with
// NewKey and ValidateKeys catches an unreachable "size.one" at startup. A
// plural-only key still renders its "other" form under T, so it degrades to
// real text rather than echoing when called without a count.
//
// # HTTP
//
// [Middleware] resolves the locale of each request and puts a [Localizer] on
// the context. [MiddlewareWithConfig] takes the sources to ask, in order; the
// default locale answers when none does:
//
//	r.Use(i18n.MiddlewareWithConfig(i18n.Config[*Context]{
//	    Bundle: bundle,
//	    Sources: []i18n.Source{
//	        i18n.FromParam("lang"),
//	        i18n.FromCookie("lang"),
//	        i18n.FromAcceptLanguage(),
//	    },
//	}))
//
// A handler context is a context.Context, so the context functions take it as
// it stands, and so does the context a component renders with:
//
//	func show(c *Context) error {
//	    title := i18n.T(c, "app.title")
//	    price := i18n.Currency(c, i18n.Money{Minor: 1999, Digits: 2, Symbol: "$"})
//	    ...
//	}
//
// [FromFunc] reads the context type of the application, so a locale that a
// signed-in user saved needs no lookup.
//
// # Locale in the path
//
// [Bundle.Has] makes a parameter class of the locales the catalogs define, so
// /xx is a 404 and each locale has one spelling. [RedirectToLocale] sends the
// root to the locale of the visitor:
//
//	r.ParamClass("locale", bundle.Has)
//	r.GET("/", i18n.RedirectToLocale[*Context](bundle, "/{lang}", "lang"))
//	r.Route("/{lang:locale}", func(g *router.Router[*Context]) { ... })
//
// A link keeps the locale with router.MustExpand:
//
//	router.MustExpand("/{lang}/posts/{id}", "lang", i18n.LocaleFrom(c).Tag(), "id", id)
//
// # Background jobs
//
// Code outside a request stamps the context itself and uses the same calls:
//
//	ctx = bundle.WithLocale(ctx, bundle.ParseOrDefault(user.Locale))
//
// A context with no Localizer fails closed: messages echo their key and
// values format invariantly. Nothing panics.
//
// # Templates
//
// Localizer is a two-word value with the full method set and the locale
// already resolved. Embed it in html/template data:
//
//	tmpl.Execute(w, map[string]any{"L": i18n.From(c)})
//	// {{ .L.T "app.title" }}
//
// # Relative time
//
// This package has no relative-time helper. "3 hours ago" is a threshold
// ladder — where "just now" ends, whether day one reads "yesterday" — and
// those are product decisions. Express it with plurals, one key per unit:
//
//	{"time_ago": {"minutes": {"zero": "just now", "one": "a minute ago",
//	                          "other": "{{count}} minutes ago"}}}
//
// then pick the unit and call TN.
//
// # Concurrency
//
// A Bundle is immutable after New and safe for concurrent use. Rendering
// never returns an error and never panics.
package i18n
