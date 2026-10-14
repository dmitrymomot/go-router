package i18n

import (
	"errors"
	"maps"
	"os"
	"slices"
	"testing"
	"testing/fstest"
)

func TestLoadFS(t *testing.T) {
	cats, err := loadFS(os.DirFS("testdata/locales"))
	if err != nil {
		t.Fatalf("loadFS: %v", err)
	}

	// The locale set is exactly the directories present, including vi, a
	// language this package knows nothing about.
	if got, want := slices.Sorted(maps.Keys(cats)), []string{"de", "en", "en-GB", "uk", "vi"}; !slices.Equal(got, want) {
		t.Fatalf("locales = %v, want %v", got, want)
	}

	en := cats["en"]
	// Namespace becomes the leading key segment; nesting flattens to dots.
	for key, want := range map[string]string{
		"app.title":          "Dashboard",
		"app.greeting":       "Hello, {{name}}!",
		"app.buttons.save":   "Save",
		"app.buttons.cancel": "Cancel",
	} {
		if got := en.messages[key]; got != want {
			t.Errorf("en messages[%q] = %q, want %q", key, got, want)
		}
	}

	// A map of plural forms becomes a plural entry, not a namespace.
	if _, ok := en.messages["cart.items"]; ok {
		t.Error("cart.items was stored as a message, want a plural")
	}
	forms := en.plurals["cart.items"]
	for cat, want := range map[PluralCategory]string{
		Zero:  "Your cart is empty",
		One:   "1 item in your cart",
		Other: "{{count}} items in your cart",
	} {
		if got := forms[cat]; got != want {
			t.Errorf("en cart.items[%v] = %q, want %q", cat, got, want)
		}
	}

	// uk/cart.json has no "other" form; loading must not invent one.
	ukForms := cats["uk"].plurals["cart.items"]
	if _, ok := ukForms[Other]; ok {
		t.Error("uk cart.items gained an other form")
	}
	if got, want := ukForms[Many], "{{count}} товарів у кошику"; got != want {
		t.Errorf("uk cart.items[many] = %q, want %q", got, want)
	}

	// Directory name is normalized into the tag.
	if got, want := cats["en-GB"].messages["app.title"], "Dashboard (GB)"; got != want {
		t.Errorf("en-GB app.title = %q, want %q", got, want)
	}
}

func TestIsPluralMap(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]any
		want bool
	}{
		{"one and other", map[string]any{"one": "x", "other": "y"}, true},
		{"other only", map[string]any{"other": "y"}, true},
		{"namespace", map[string]any{"save": "Save"}, false},
		{"mixed keys", map[string]any{"one": "x", "save": "y"}, false},
		{"category key holding an object", map[string]any{"other": map[string]any{"a": "b"}}, false},
		{"category key holding a number", map[string]any{"other": 1.0}, false},
		{"empty", map[string]any{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPluralMap(tc.in); got != tc.want {
				t.Errorf("isPluralMap(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func catalogFS(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for name, data := range files {
		m[name] = &fstest.MapFile{Data: []byte(data)}
	}
	return m
}

func TestLoadFSErrors(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  error
	}{
		{"bad json", map[string]string{"en/app.json": "{oops"}, ErrInvalidCatalog},
		// encoding/json/v2 rejects a repeated object name instead of keeping the last.
		{"duplicate json name", map[string]string{"en/app.json": `{"a": "1", "a": "2"}`}, ErrInvalidCatalog},
		{"invalid utf-8", map[string]string{"en/app.json": "{\"a\": \"\xff\"}"}, ErrInvalidCatalog},
		{"unnormalizable dir", map[string]string{"--/app.json": "{}"}, ErrInvalidCatalog},
		{"top level not an object", map[string]string{"en/app.json": `["a"]`}, ErrInvalidCatalog},
		// A plural entry and a plain message cannot share a key.
		{"plural and form message", map[string]string{"en/app.json": `{"x": {"one": "a", "other": "b"}, "x.one": "c"}`}, ErrDuplicateKey},
		{"dotted name and nested key", map[string]string{"en/app.json": `{"a.b": "1", "a": {"b": "2"}}`}, ErrDuplicateKey},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadFS(catalogFS(tc.files))
			if !errors.Is(err, tc.want) {
				t.Errorf("loadFS error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestLoadFSSameKeyInDifferentNamespaces(t *testing.T) {
	cats, err := loadFS(catalogFS(map[string]string{
		"en/app.json":  `{"x": "1"}`,
		"en/app2.json": `{"x": "2"}`,
	}))
	if err != nil {
		t.Fatalf("loadFS: %v", err)
	}
	if got := cats["en"].messages; got["app.x"] != "1" || got["app2.x"] != "2" {
		t.Errorf("messages = %v, want app.x=1 and app2.x=2", got)
	}
}

func TestLoadFSScalars(t *testing.T) {
	cats, err := loadFS(catalogFS(map[string]string{"en/app.json": `{"n": 42, "b": true}`}))
	if err != nil {
		t.Fatalf("loadFS: %v", err)
	}
	en := cats["en"]
	if got := en.messages["app.n"]; got != "42" {
		t.Errorf("app.n = %q, want 42", got)
	}
	if got := en.messages["app.b"]; got != "true" {
		t.Errorf("app.b = %q, want true", got)
	}
}

func TestLoadFSIgnoresNonCatalogFiles(t *testing.T) {
	cats, err := loadFS(catalogFS(map[string]string{
		"en/app.json":       `{"a": "b"}`,
		"en/README.md":      "not json",
		"README.md":         "not json",
		"top.json":          "{oops",
		"en/deep/more.json": "{oops",
	}))
	if err != nil {
		t.Fatalf("loadFS: %v", err)
	}
	if len(cats) != 1 {
		t.Fatalf("loaded %d locales, want 1", len(cats))
	}
	if got := cats["en"].messages["app.a"]; got != "b" {
		t.Errorf("app.a = %q, want b", got)
	}
}

type failFS struct{ fstest.MapFS }

func (failFS) ReadFile(string) ([]byte, error) { return nil, errors.New("disk on fire") }

func TestLoadFSReadError(t *testing.T) {
	_, err := loadFS(failFS{catalogFS(map[string]string{"en/app.json": "{}"})})
	if !errors.Is(err, ErrInvalidCatalog) {
		t.Errorf("loadFS error = %v, want ErrInvalidCatalog", err)
	}
}

func TestSetPluralErrors(t *testing.T) {
	rc := newRawCatalog()
	if err := rc.setPlural("k", map[string]any{"bogus": "x"}); !errors.Is(err, ErrInvalidCatalog) {
		t.Errorf("non-category form error = %v, want ErrInvalidCatalog", err)
	}
	if err := rc.setPlural("k", map[string]any{"one": 1.0}); !errors.Is(err, ErrInvalidCatalog) {
		t.Errorf("non-string form error = %v, want ErrInvalidCatalog", err)
	}
}

func TestSetMessageCollidesWithNestedPluralForm(t *testing.T) {
	// The plural at app.x is flattened first, then the message app.x.one
	// arrives: the collision must be caught from this order as well.
	rc := newRawCatalog()
	if err := rc.putPlural("app.x", map[PluralCategory]string{One: "a"}); err != nil {
		t.Fatalf("putPlural: %v", err)
	}
	if err := rc.setMessage("app.x.one", "shadow"); !errors.Is(err, ErrDuplicateKey) {
		t.Errorf("setMessage error = %v, want ErrDuplicateKey", err)
	}
}
