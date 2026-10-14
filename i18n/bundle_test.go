package i18n_test

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/dmitrymomot/go-router/i18n"
)

// testLocales is the shared catalog fixture: en, en-GB, uk, de, vi.
func testLocales() fs.FS { return os.DirFS("testdata/locales") }

func newBundle(t *testing.T, opts ...i18n.Option) *i18n.Bundle {
	t.Helper()
	base := []i18n.Option{i18n.WithMessages(testLocales())}
	b, err := i18n.New("en", append(base, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

// mapFS builds a catalog file system from a path-to-JSON map.
func mapFS(files map[string]string) fstest.MapFS {
	m := make(fstest.MapFS, len(files))
	for name, data := range files {
		m[name] = &fstest.MapFile{Data: []byte(data)}
	}
	return m
}

// mustPanic fails the test unless f panics with a message containing want.
func mustPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Errorf("did not panic, want a panic containing %q", want)
			return
		}
		if msg, _ := r.(string); !strings.Contains(msg, want) {
			t.Errorf("panic = %v, want it to contain %q", r, want)
		}
	}()
	f()
}

func TestT(t *testing.T) {
	b := newBundle(t)
	en := b.ParseOrDefault("en")
	uk := b.ParseOrDefault("uk")

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"default locale", b.T(en, "app.title"), "Dashboard"},
		{"argument", b.T(uk, "app.greeting", "name", "Олю"), "Привіт, Олю!"},
		{"nested key", b.T(uk, "app.buttons.save"), "Зберегти"},
		// uk lacks only_en, so it falls through to the default locale.
		{"falls through to default", b.T(uk, "app.only_en"), "English only"},
		// A key nobody defines echoes back.
		{"missing key echoes", b.T(en, "app.nope"), "app.nope"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestTSupportsUnknownLanguage(t *testing.T) {
	// vi is a language this package ships no knowledge of. It must work
	// exactly like any other.
	b := newBundle(t)
	vi, err := b.Parse("vi")
	if err != nil {
		t.Fatalf("Parse(vi): %v", err)
	}
	if got := vi.Tag(); got != "vi" {
		t.Errorf("Tag() = %q, want vi", got)
	}
	if got, want := b.T(vi, "app.title"), "Bảng điều khiển"; got != want {
		t.Errorf("T(app.title) = %q, want %q", got, want)
	}
	// A key vi lacks falls through to the default.
	if got, want := b.T(vi, "app.only_en"), "English only"; got != want {
		t.Errorf("T(app.only_en) = %q, want %q", got, want)
	}
}

func TestRegionalLayering(t *testing.T) {
	b := newBundle(t)
	gb, err := b.Parse("en-GB")
	if err != nil {
		t.Fatalf("Parse(en-GB): %v", err)
	}
	// en-GB defines title...
	if got, want := b.T(gb, "app.title"), "Dashboard (GB)"; got != want {
		t.Errorf("T(app.title) = %q, want %q", got, want)
	}
	// ...but not greeting, which layers through to en.
	if got, want := b.T(gb, "app.greeting", "name", "Ann"), "Hello, Ann!"; got != want {
		t.Errorf("T(app.greeting) = %q, want %q", got, want)
	}
}

func TestParse(t *testing.T) {
	b := newBundle(t)

	tests := []struct {
		in   string
		want string
	}{
		{"uk", "uk"},
		// Normalization at the boundary.
		{"EN_gb", "en-GB"},
		// Region falls back to base language, and Parse reports what you actually got.
		{"uk-UA", "uk"},
	}
	for _, tc := range tests {
		loc, err := b.Parse(tc.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.in, err)
			continue
		}
		if got := loc.Tag(); got != tc.want {
			t.Errorf("Parse(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Unsupported and malformed fail.
	for _, in := range []string{"ww-WW", "", "-"} {
		if _, err := b.Parse(in); !errors.Is(err, i18n.ErrUnknownLocale) {
			t.Errorf("Parse(%q) error = %v, want ErrUnknownLocale", in, err)
		}
	}

	// ParseOrDefault never fails.
	for _, in := range []string{"ww-WW", ""} {
		if got := b.ParseOrDefault(in); got != b.Default() {
			t.Errorf("ParseOrDefault(%q) = %v, want the default", in, got)
		}
	}
	if got := b.Default().Tag(); got != "en" {
		t.Errorf("Default() = %q, want en", got)
	}
}

func TestLocales(t *testing.T) {
	b := newBundle(t)
	var tags []string
	for _, l := range b.Locales() {
		tags = append(tags, l.Tag())
	}
	// Locales documents "sorted by tag", which is also what makes the locale
	// indices behind it deterministic across runs.
	if want := []string{"de", "en", "en-GB", "uk", "vi"}; !slices.Equal(tags, want) {
		t.Errorf("Locales() = %v, want %v", tags, want)
	}
}

func TestZeroLocaleUsesDefault(t *testing.T) {
	b := newBundle(t)
	var zero i18n.Locale
	if got, want := b.T(zero, "app.title"), "Dashboard"; got != want {
		t.Errorf("T(zero, app.title) = %q, want %q", got, want)
	}
}

func TestTNDefaultRule(t *testing.T) {
	// No rule wired for en: the built-in zero-one-many default applies.
	b := newBundle(t)
	en := b.Default()
	tests := []struct {
		n    int
		want string
	}{
		// zero form defined in the catalog and DefaultRule(0) == Zero.
		{0, "Your cart is empty"},
		{1, "1 item in your cart"},
		// DefaultRule(5) == Many; en has no many form, so many->other resolves it.
		{5, "5 items in your cart"},
	}
	for _, tc := range tests {
		if got := b.TN(en, "cart.items", tc.n); got != tc.want {
			t.Errorf("TN(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// ukRule is a real Ukrainian rule declared inline: this package ships no
// language data, and these tests must not depend on cldr.
func ukRule(n int) i18n.PluralCategory {
	if n < 0 {
		n = -n
	}
	m10, m100 := n%10, n%100
	switch {
	case m10 == 1 && m100 != 11:
		return i18n.One
	case m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14):
		return i18n.Few
	default:
		return i18n.Many
	}
}

func TestTNWiredRule(t *testing.T) {
	b := newBundle(t, i18n.WithPlural("uk", ukRule))
	uk := b.ParseOrDefault("uk")

	tests := []struct {
		n    int
		want string
	}{
		{1, "1 товар у кошику"},
		{21, "21 товар у кошику"}, // 21 -> one, not many
		{2, "2 товари у кошику"},
		{5, "5 товарів у кошику"},
		{11, "11 товарів у кошику"},
	}
	for _, tc := range tests {
		if got := b.TN(uk, "cart.items", tc.n); got != tc.want {
			t.Errorf("TN(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
	// uk's rule never produces Other, and uk/cart.json defines no other form.
	// Nothing above needed one, which is correct, not a gap.
}

func TestTNUsesRuleOfLocaleWhereMessageFound(t *testing.T) {
	// de has no cart.json, so cart.items resolves through the default (en)
	// catalog. The form must be selected with en's rule (the rule of the
	// locale the message was FOUND in), not with de's.
	//
	// de's rule sends every count to Many, and en's catalog defines no many
	// form: selecting with de's rule would fall back many->other and render
	// "1 items in your cart". en's own DefaultRule sends 1 to One, which en
	// does define. The two answers differ, so this pins the decision down.
	alwaysMany := func(int) i18n.PluralCategory { return i18n.Many }
	b := newBundle(t, i18n.WithPlural("de", alwaysMany))
	de := b.ParseOrDefault("de")
	if got, want := b.TN(de, "cart.items", 1), "1 item in your cart"; got != want {
		t.Errorf("TN(1) = %q, want %q", got, want)
	}
}

func TestWithPluralResolvesTagThenLanguage(t *testing.T) {
	// A rule wired for "pt" covers every pt locale; one wired for "pt-PT"
	// overrides it there alone. CLDR needs exactly this: Portuguese reads 0 as
	// one, European Portuguese reads it as other.
	zeroOne := func(n int) i18n.PluralCategory {
		if n == 0 || n == 1 || n == -1 {
			return i18n.One
		}
		return i18n.Other
	}
	oneOnly := func(n int) i18n.PluralCategory {
		if n == 1 || n == -1 {
			return i18n.One
		}
		return i18n.Other
	}
	items := `{"items": {"one": "{{count}} item", "other": "{{count}} itens"}}`
	b, err := i18n.New("en",
		i18n.WithMessages(mapFS(map[string]string{
			"en/cart.json":    `{"items": {"one": "1 item", "other": "{{count}} items"}}`,
			"pt/cart.json":    items,
			"pt-BR/cart.json": items,
			"pt-PT/cart.json": items,
		})),
		i18n.WithPlural("pt", zeroOne),
		i18n.WithPlural("pt_pt", oneOnly), // normalized like any tag
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for tag, want := range map[string]string{
		"pt":    "0 item",
		"pt-BR": "0 item",
		"pt-PT": "0 itens",
	} {
		if got := b.TN(b.ParseOrDefault(tag), "cart.items", 0); got != want {
			t.Errorf("%s: TN(0) = %q, want %q", tag, got, want)
		}
	}
}

func TestWithPluralRegionalRuleSkipsBaseLanguage(t *testing.T) {
	// A rule wired for "pt-BR" alone leaves a bare pt locale on DefaultRule,
	// which reads 0 as zero and so falls to "other" in a {one, other} catalog.
	one := func(int) i18n.PluralCategory { return i18n.One }
	b, err := i18n.New("en",
		i18n.WithMessages(mapFS(map[string]string{
			"en/cart.json":    `{"items": {"one": "1 item", "other": "{{count}} items"}}`,
			"pt/cart.json":    `{"items": {"one": "{{count}} item", "other": "{{count}} itens"}}`,
			"pt-BR/cart.json": `{"items": {"one": "{{count}} item BR", "other": "{{count}} itens BR"}}`,
		})),
		i18n.WithPlural("pt-BR", one),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := b.TN(b.ParseOrDefault("pt-BR"), "cart.items", 5), "5 item BR"; got != want {
		t.Errorf("pt-BR: TN(5) = %q, want %q", got, want)
	}
	if got, want := b.TN(b.ParseOrDefault("pt"), "cart.items", 5), "5 itens"; got != want {
		t.Errorf("pt: TN(5) = %q, want %q", got, want)
	}
}

func TestTNExplicitCountOverride(t *testing.T) {
	b := newBundle(t)
	if got, want := b.TN(b.Default(), "cart.items", 5, "count", "many"), "many items in your cart"; got != want {
		t.Errorf("TN = %q, want %q", got, want)
	}
}

func TestTNFallsBackAcrossLocales(t *testing.T) {
	b := newBundle(t)
	// de has no cart.json at all: the whole plural entry resolves via the
	// default locale's catalog.
	de := b.ParseOrDefault("de")
	if got, want := b.TN(de, "cart.items", 3), "3 items in your cart"; got != want {
		t.Errorf("TN(3) = %q, want %q", got, want)
	}
}

func TestMissHandler(t *testing.T) {
	var mu sync.Mutex
	var misses []i18n.Miss
	b := newBundle(t, i18n.WithMissingHandler(func(m i18n.Miss) {
		mu.Lock()
		defer mu.Unlock()
		misses = append(misses, m)
	}))
	if got := b.T(b.Default(), "app.nope"); got != "app.nope" {
		t.Errorf("T = %q, want the key echoed", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(misses) != 1 {
		t.Fatalf("got %d misses, want 1: %v", len(misses), misses)
	}
	m := misses[0]
	if m.Key != "app.nope" || m.Reason != i18n.MissingKey || m.Locale.Tag() != "en" {
		t.Errorf("miss = %+v, want key app.nope, reason MissingKey, locale en", m)
	}
}

// formGaps collects the MissingForm keys New reports, sorted.
func formGaps(keys *[]string, mu *sync.Mutex) i18n.Option {
	return i18n.WithMissingHandler(func(m i18n.Miss) {
		if m.Reason == i18n.MissingForm {
			mu.Lock()
			*keys = append(*keys, m.Key)
			mu.Unlock()
		}
	})
}

func TestProbeOnlyRunsForWiredRules(t *testing.T) {
	// With no rule wired, DefaultRule applies and probing must stay silent:
	// an ordinary {zero,one,other} catalog is not "incomplete" with respect to
	// a rule that is not a grammar claim.
	var mu sync.Mutex
	var keys []string
	newBundle(t, formGaps(&keys, &mu))
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 0 {
		t.Errorf("probing fired for default-rule locales: %v", keys)
	}
}

func TestProbeReportsIncompleteTranslation(t *testing.T) {
	// A rule producing one/few/many against a catalog defining only one/few:
	// "many" is a real gap and must be reported.
	rule := func(n int) i18n.PluralCategory {
		switch {
		case n == 1:
			return i18n.One
		case n >= 2 && n <= 4:
			return i18n.Few
		default:
			return i18n.Many
		}
	}
	var mu sync.Mutex
	var keys []string
	_, err := i18n.New("en",
		i18n.WithMessages(mapFS(map[string]string{"en/app.json": `{"n": {"one": "a", "few": "b"}}`})),
		i18n.WithPlural("en", rule),
		formGaps(&keys, &mu),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"app.n.many"}; !slices.Equal(keys, want) {
		t.Errorf("reported %v, want %v", keys, want)
	}
}

func TestProbeReportsDeadForm(t *testing.T) {
	// A rule that never produces "two" against a catalog defining it: dead
	// translation. But "zero" (convention) and "other" (terminal) never count.
	rule := func(n int) i18n.PluralCategory {
		if n == 1 {
			return i18n.One
		}
		return i18n.Other
	}
	var mu sync.Mutex
	var keys []string
	_, err := i18n.New("en",
		i18n.WithMessages(mapFS(map[string]string{
			"en/app.json": `{"n": {"one": "a", "two": "b", "other": "c", "zero": "d"}}`,
		})),
		i18n.WithPlural("en", rule),
		formGaps(&keys, &mu),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"app.n.two"}; !slices.Equal(keys, want) {
		t.Errorf("reported %v, want %v (zero and other are never dead)", keys, want)
	}
}

func TestZeroConventionBeatsWiredRule(t *testing.T) {
	// A rule sending 0 to "other", with a catalog that defines "zero": the
	// translator's zero form wins.
	rule := func(n int) i18n.PluralCategory {
		if n == 1 {
			return i18n.One
		}
		return i18n.Other
	}
	b := newBundle(t, i18n.WithPlural("en", rule))
	if got, want := b.TN(b.Default(), "cart.items", 0), "Your cart is empty"; got != want {
		t.Errorf("TN(0) = %q, want %q", got, want)
	}
}

func TestAppendT(t *testing.T) {
	b := newBundle(t)
	en := b.Default()
	if got, want := string(b.AppendT([]byte("x:"), en, "app.title")), "x:Dashboard"; got != want {
		t.Errorf("AppendT = %q, want %q", got, want)
	}
	if got, want := string(b.AppendTN([]byte("x:"), en, "cart.items", 1)), "x:1 item in your cart"; got != want {
		t.Errorf("AppendTN = %q, want %q", got, want)
	}
	// A plural-only key without a count renders its other form.
	if got, want := string(b.AppendT([]byte("x:"), en, "cart.items")), "x:{{count}} items in your cart"; got != want {
		t.Errorf("AppendT(plural key) = %q, want %q", got, want)
	}
}

func TestAppendMissEchoesKey(t *testing.T) {
	b := newBundle(t)
	en := b.Default()
	if got, want := string(b.AppendT([]byte("x:"), en, "app.nope")), "x:app.nope"; got != want {
		t.Errorf("AppendT = %q, want %q", got, want)
	}
	if got, want := string(b.AppendTN([]byte("x:"), en, "app.nope", 2)), "x:app.nope"; got != want {
		t.Errorf("AppendTN = %q, want %q", got, want)
	}
}

func TestTNMissEchoesKey(t *testing.T) {
	b := newBundle(t)
	if got := b.TN(b.Default(), "app.nope", 2); got != "app.nope" {
		t.Errorf("TN = %q, want the key echoed", got)
	}
}

func TestTNRendersPlainMessage(t *testing.T) {
	// A key with no plural forms still renders under TN, with count injected,
	// so a catalog may later promote it to plural forms without breaking
	// callers that already say TN.
	b := newBundle(t)
	en := b.Default()
	if got, want := b.TN(en, "app.title", 3), "Dashboard"; got != want {
		t.Errorf("TN = %q, want %q", got, want)
	}
	if got, want := string(b.AppendTN([]byte("x:"), en, "app.title", 3)), "x:Dashboard"; got != want {
		t.Errorf("AppendTN = %q, want %q", got, want)
	}
}

func TestLocaleFromAnotherBundleResolvesByLanguage(t *testing.T) {
	// A Locale is globally meaningful: the same tag means the same thing in
	// every Bundle. One bundle's regional Locale used against a bundle that
	// only carries the base language must layer down to it, not silently
	// become the default.
	regional, err := i18n.New("en", i18n.WithMessages(mapFS(map[string]string{
		"en/app.json":    `{"k": "en"}`,
		"pt-BR/app.json": `{"k": "pt-BR"}`,
		"de/app.json":    `{"k": "de"}`,
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ptBR, err := regional.Parse("pt-BR")
	if err != nil || ptBR.Tag() != "pt-BR" {
		t.Fatalf("Parse(pt-BR) = %v, %v", ptBR, err)
	}

	base, err := i18n.New("en", i18n.WithMessages(mapFS(map[string]string{
		"en/app.json": `{"k": "en"}`,
		"pt/app.json": `{"k": "pt"}`,
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := base.T(ptBR, "app.k"); got != "pt" {
		t.Errorf("T(pt-BR) = %q, want pt", got)
	}

	// A language the second bundle carries no catalog for at all falls all the
	// way to its default.
	de, err := regional.Parse("de")
	if err != nil {
		t.Fatalf("Parse(de): %v", err)
	}
	if got := base.T(de, "app.k"); got != "en" {
		t.Errorf("T(de) = %q, want en", got)
	}
}

func TestRuleReturningUnknownCategoryDoesNotPanic(t *testing.T) {
	// Rules are caller-supplied, so a bad one must degrade, not crash: an
	// out-of-range category resolves to nothing and the chain carries on.
	junk := func(int) i18n.PluralCategory { return i18n.PluralCategory(200) }
	b := newBundle(t, i18n.WithPlural("uk", junk))
	uk := b.ParseOrDefault("uk")
	// uk's forms are unreachable via a junk category, so the lookup falls
	// through to en, which resolves with en's own rule.
	if got, want := b.TN(uk, "cart.items", 1), "1 item in your cart"; got != want {
		t.Errorf("TN(1) = %q, want %q", got, want)
	}
}

func TestNewErrors(t *testing.T) {
	tests := []struct {
		name    string
		def     string
		opts    []i18n.Option
		wantErr error
	}{
		{"no messages", "en", nil, i18n.ErrInvalidCatalog},
		{"default locale absent from catalogs", "fr", []i18n.Option{i18n.WithMessages(testLocales())}, i18n.ErrInvalidCatalog},
		{"default locale given as a regional tag the catalogs lack", "en-US", []i18n.Option{i18n.WithMessages(testLocales())}, i18n.ErrInvalidCatalog},
		{"bad json", "en", []i18n.Option{i18n.WithMessages(mapFS(map[string]string{"en/app.json": "{oops"}))}, i18n.ErrInvalidCatalog},
		{"duplicate json name", "en", []i18n.Option{i18n.WithMessages(mapFS(map[string]string{"en/app.json": `{"a": "1", "a": "2"}`}))}, i18n.ErrInvalidCatalog},
		{"invalid utf-8", "en", []i18n.Option{i18n.WithMessages(mapFS(map[string]string{"en/app.json": "{\"a\": \"\xff\"}"}))}, i18n.ErrInvalidCatalog},
		{"directory is not a tag", "en", []i18n.Option{i18n.WithMessages(mapFS(map[string]string{"--/app.json": "{}"}))}, i18n.ErrInvalidCatalog},
		{"same inline key twice", "en", []i18n.Option{i18n.WithTranslations("en", "app", map[string]any{"x": "1"}), i18n.WithTranslations("en", "app", map[string]any{"x": "again"})}, i18n.ErrDuplicateKey},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := i18n.New(tc.def, tc.opts...)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("New error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestNewMergeErrors covers collisions between two sources for one locale.
// Each source is internally consistent, so only the merge can catch these.
func TestNewMergeErrors(t *testing.T) {
	tests := map[string]struct{ a, b string }{
		"same message key":         {`{"n": "one"}`, `{"n": "two"}`},
		"same plural key":          {`{"n": {"one": "a"}}`, `{"n": {"one": "b"}}`},
		"plural then message":      {`{"n": {"one": "a"}}`, `{"n": "plain"}`},
		"message then plural":      {`{"n": "plain"}`, `{"n": {"one": "a"}}`},
		"plural then form message": {`{"n": {"one": "a"}}`, `{"n.one": "shadow"}`},
		"form message then plural": {`{"n.one": "shadow"}`, `{"n": {"one": "a"}}`},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := i18n.New("en",
				i18n.WithMessages(mapFS(map[string]string{"en/app.json": tc.a})),
				i18n.WithMessages(mapFS(map[string]string{"en/app.json": tc.b})),
			)
			if !errors.Is(err, i18n.ErrDuplicateKey) {
				t.Errorf("New error = %v, want ErrDuplicateKey", err)
			}
		})
	}
}

// TestCategoryKeyedNamespaceIsPlural pins the deterministic plural-detection
// rule and its one sharp edge: a namespace whose keys are only category words
// is parsed as a plural, so its entries are reachable via TN, T degrades to the
// "other" form rather than echoing, and ValidateKeys flags the unreachable
// "app.size.one" at startup instead of letting it echo silently.
func TestCategoryKeyedNamespaceIsPlural(t *testing.T) {
	b, err := i18n.New("en", i18n.WithTranslations("en", "app", map[string]any{
		"size": map[string]any{"one": "One size", "other": "Other sizes"},
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	en := b.Default()

	// Detected as a plural: TN selects a form by count.
	if got := b.TN(en, "app.size", 1); got != "One size" {
		t.Errorf("TN(1) = %q, want One size", got)
	}
	if got := b.TN(en, "app.size", 3); got != "Other sizes" {
		t.Errorf("TN(3) = %q, want Other sizes", got)
	}
	// T carries no count, so it renders the "other" form, not an echo.
	if got := b.T(en, "app.size"); got != "Other sizes" {
		t.Errorf("T = %q, want Other sizes", got)
	}
	// "app.size.one" is not a reachable message key; ValidateKeys surfaces the
	// misfiled namespace at startup.
	if err := b.ValidateKeys(i18n.NewKey("app.size.one")); !errors.Is(err, i18n.ErrUnknownKey) {
		t.Errorf("ValidateKeys error = %v, want ErrUnknownKey", err)
	}
}

// TestNewMergesDistinctSources is the companion to TestNewMergeErrors: two
// sources that do not collide must layer into one catalog.
func TestNewMergesDistinctSources(t *testing.T) {
	b, err := i18n.New("en",
		i18n.WithMessages(mapFS(map[string]string{"en/app.json": `{"a": "A"}`})),
		i18n.WithMessages(mapFS(map[string]string{"en/other.json": `{"b": "B"}`})),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := b.T(b.Default(), "app.a"); got != "A" {
		t.Errorf("T(app.a) = %q, want A", got)
	}
	if got := b.T(b.Default(), "other.b"); got != "B" {
		t.Errorf("T(other.b) = %q, want B", got)
	}
}

func TestWithTranslations(t *testing.T) {
	b, err := i18n.New("en",
		i18n.WithTranslations("en", "app", map[string]any{
			"title": "Programmatic",
			"nest":  map[string]any{"k": "v"},
		}),
		// A second call for the same locale adds a namespace.
		i18n.WithTranslations("EN", "extra", map[string]any{"k": "w"}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for key, want := range map[string]string{"app.title": "Programmatic", "app.nest.k": "v", "extra.k": "w"} {
		if got := b.T(b.Default(), key); got != want {
			t.Errorf("T(%s) = %q, want %q", key, got, want)
		}
	}
}

func TestWithTranslationsAddsToMessages(t *testing.T) {
	b := newBundle(t, i18n.WithTranslations("fr", "app", map[string]any{"title": "Tableau"}))
	fr, err := b.Parse("fr")
	if err != nil {
		t.Fatalf("Parse(fr): %v", err)
	}
	if got := b.T(fr, "app.title"); got != "Tableau" {
		t.Errorf("T = %q, want Tableau", got)
	}
}

func TestMissReasonString(t *testing.T) {
	if got := i18n.MissingKey.String(); got != "missing_key" {
		t.Errorf("MissingKey.String() = %q", got)
	}
	if got := i18n.MissingForm.String(); got != "missing_form" {
		t.Errorf("MissingForm.String() = %q", got)
	}
}

func TestBundleConcurrentReads(t *testing.T) {
	b := newBundle(t)
	en, uk := b.ParseOrDefault("en"), b.ParseOrDefault("uk")
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if got := b.T(en, "app.title"); got != "Dashboard" {
				t.Errorf("T(en) = %q", got)
			}
			if got := b.T(uk, "app.title"); got != "Панель" {
				t.Errorf("T(uk) = %q", got)
			}
			if got := b.TN(en, "cart.items", 1); got != "1 item in your cart" {
				t.Errorf("TN(en) = %q", got)
			}
		})
	}
	wg.Wait()
}
