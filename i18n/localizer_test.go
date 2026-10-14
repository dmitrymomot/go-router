package i18n_test

import (
	"reflect"
	"testing"
	"unsafe"

	"github.com/dmitrymomot/go-router/i18n"
)

func TestLocalizer(t *testing.T) {
	b := fmtBundle(t)
	l := b.For(b.ParseOrDefault("uk"))

	if got := l.Locale().Tag(); got != "uk" {
		t.Errorf("Locale() = %q, want uk", got)
	}
	if l.IsZero() {
		t.Error("IsZero() = true for a bound Localizer")
	}
	if got, want := l.T("app.title"), "Панель"; got != want {
		t.Errorf("T = %q, want %q", got, want)
	}
	if got, want := l.T("app.greeting", "name", "Олю"), "Привіт, Олю!"; got != want {
		t.Errorf("T(greeting) = %q, want %q", got, want)
	}
	// Falls through to the default, exactly like Bundle.T.
	if got, want := l.T("app.only_en"), "English only"; got != want {
		t.Errorf("T(only_en) = %q, want %q", got, want)
	}
}

// TestLocalizerMatchesBundle is the falsifiable delegation check: en, de, uk
// and vi each render numbers and dates differently (Invariant vs. deSpec vs.
// frSpec), so a Localizer that cached the wrong locale index, or that
// re-implemented formatting instead of delegating, would diverge from the
// equivalent Bundle call for at least one of them.
func TestLocalizerMatchesBundle(t *testing.T) {
	b := fmtBundle(t)
	m := eur(123456)

	for _, tag := range []string{"en", "de", "uk", "vi"} {
		loc := b.ParseOrDefault(tag)
		l := b.For(loc)
		tests := []struct {
			name      string
			got, want string
		}{
			{"T", l.T("app.title"), b.T(loc, "app.title")},
			{"TN", l.TN("cart.items", 3), b.TN(loc, "cart.items", 3)},
			{"Number", l.Number(1234.5), b.Number(loc, 1234.5)},
			{"NumberInt", l.NumberInt(1234), b.NumberInt(loc, 1234)},
			{"Percent", l.Percent(0.5), b.Percent(loc, 0.5)},
			{"Currency", l.Currency(m), b.Currency(loc, m)},
			{"Date", l.Date(refTS), b.Date(loc, refTS)},
			{"Time", l.Time(refTS), b.Time(loc, refTS)},
			{"DateTime", l.DateTime(refTS), b.DateTime(loc, refTS)},
		}
		for _, tc := range tests {
			if tc.got != tc.want {
				t.Errorf("%s: Localizer.%s = %q, Bundle.%s = %q", tag, tc.name, tc.got, tc.name, tc.want)
			}
		}
	}
}

func TestLocalizerKeys(t *testing.T) {
	b := fmtBundle(t)
	l := b.For(b.Default())
	if got, want := l.TK(keyTitle), "Dashboard"; got != want {
		t.Errorf("TK = %q, want %q", got, want)
	}
	if got, want := l.TNK(keyItems, 1), "1 item in your cart"; got != want {
		t.Errorf("TNK = %q, want %q", got, want)
	}
}

func TestZeroLocalizerIsSafe(t *testing.T) {
	// The zero Localizer must fail closed, never panic.
	var l i18n.Localizer
	if !l.IsZero() || !l.Locale().IsZero() {
		t.Error("zero Localizer is not zero")
	}

	tests := []struct {
		name      string
		got, want string
	}{
		{"T", l.T("app.title"), "app.title"},
		{"TN", l.TN("cart.items", 5), "cart.items"},
		{"TK", l.TK(keyTitle), "app.title"},
		{"TNK", l.TNK(keyItems, 5), "cart.items"},
		// Formatting falls back to Invariant.
		{"Number", l.Number(1234.5), "1,234.5"},
		{"NumberInt", l.NumberInt(1234), "1,234"},
		{"Percent", l.Percent(0.5), "50%"},
		{"Currency", l.Currency(usd(100)), "$1.00"},
		{"Date", l.Date(refTS), "2026-07-17"},
		{"Time", l.Time(refTS), "15:04"},
		{"DateTime", l.DateTime(refTS), "2026-07-17 15:04"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestLocalizerIsTwoWords asserts the real size of Localizer: exactly two
// machine words (a *Bundle pointer plus the cached locale index), on any
// GOARCH. It would fail if Localizer grew a third field.
func TestLocalizerIsTwoWords(t *testing.T) {
	want := 2 * unsafe.Sizeof(uintptr(0))
	if got := reflect.TypeFor[i18n.Localizer]().Size(); got != want {
		t.Errorf("Localizer is %d bytes, want %d (two machine words)", got, want)
	}
}

// TestForDoesNotAllocate: building a Localizer does no heap work (locIdx is a
// map lookup, not string construction).
func TestForDoesNotAllocate(t *testing.T) {
	b := fmtBundle(t)
	def := b.Default()
	if allocs := testing.AllocsPerRun(200, func() { _ = b.For(def) }); allocs != 0 {
		t.Errorf("For allocated %v times, want 0", allocs)
	}
}
