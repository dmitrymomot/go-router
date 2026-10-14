package cldr_test

import (
	"math"
	"testing"

	"github.com/dmitrymomot/go-router/i18n"
	"github.com/dmitrymomot/go-router/i18n/cldr"
)

// The differential test against golang.org/x/text lives in the benchmarks
// module (benchmarks/cldr_xtext_test.go), so the root module stays free of
// third-party dependencies.

// TestRulesAreTotal pins the never-panics constraint at the integer extremes.
func TestRulesAreTotal(t *testing.T) {
	for lang, rule := range cldr.All() {
		for _, n := range []int{math.MinInt, math.MinInt + 1, -1000000, -21, -1, 0, math.MaxInt} {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s(%d) panicked: %v", lang, n, r)
					}
				}()
				if got := rule(n); int(got) >= 6 {
					t.Errorf("%s(%d) = %d, an out-of-range category", lang, n, got)
				}
			}()
		}
	}
}

// TestNegativesMirrorPositives: CLDR rules are defined on the absolute value.
func TestNegativesMirrorPositives(t *testing.T) {
	for lang, rule := range cldr.All() {
		for _, n := range []int{1, 2, 5, 11, 21, 101, 1000000} {
			if rule(n) != rule(-n) {
				t.Errorf("%s: rule(%d) = %v, rule(%d) = %v, want equal", lang, n, rule(n), -n, rule(-n))
			}
		}
	}
}

// TestKnownCases pins the historical bugs this package exists to prevent,
// independently of x/text.
func TestKnownCases(t *testing.T) {
	tests := []struct {
		lang string
		n    int
		want i18n.PluralCategory
	}{
		// The Slavic family-bucket bug: uk/ru 21 is one, not many.
		{"uk", 21, i18n.One},
		{"ru", 21, i18n.One},
		{"uk", 11, i18n.Many},
		{"ru", 11, i18n.Many},
		{"uk", 0, i18n.Many},
		{"ru", 0, i18n.Many},
		{"uk", 2, i18n.Few},
		{"ru", 22, i18n.Few},
		// Polish differs from East Slavic at 21.
		{"pl", 21, i18n.Many},
		{"pl", 1, i18n.One},
		{"pl", 2, i18n.Few},
		// The Romance family-bucket bug: pt 0 is one, it 0 is other.
		{"pt", 0, i18n.One},
		{"pt-PT", 0, i18n.Other},
		{"pt-PT", 1, i18n.One},
		{"pt-BR", 0, i18n.One},
		{"it", 0, i18n.Other},
		{"fr", 0, i18n.One},
		{"es", 0, i18n.Other},
		// Arabic's six bands.
		{"ar", 0, i18n.Zero},
		{"ar", 1, i18n.One},
		{"ar", 2, i18n.Two},
		{"ar", 3, i18n.Few},
		{"ar", 11, i18n.Many},
		{"ar", 100, i18n.Other},
		// No plural distinction.
		{"ja", 1, i18n.Other},
		{"zh", 5, i18n.Other},
		{"vi", 1, i18n.Other},
		// Macedonian: CLDR's i%100!=11 exception (x/text misses this).
		{"mk", 1, i18n.One},
		{"mk", 11, i18n.Other},
		{"mk", 21, i18n.One},
		{"mk", 111, i18n.Other},
		// Hebrew: no round-tens "many" in modern CLDR (x/text is stale here).
		{"he", 1, i18n.One},
		{"he", 2, i18n.Two},
		{"he", 3, i18n.Other},
		{"he", 20, i18n.Other},
		{"he", 100, i18n.Other},
	}
	for _, tc := range tests {
		rule, ok := cldr.PluralFor(tc.lang)
		if !ok {
			t.Errorf("no rule for %s", tc.lang)
			continue
		}
		if got := rule(tc.n); got != tc.want {
			t.Errorf("%s(%d) = %v, want %v", tc.lang, tc.n, got, tc.want)
		}
	}
}

func TestPluralFor(t *testing.T) {
	r, ok := cldr.PluralFor("uk")
	if !ok {
		t.Fatal("PluralFor(uk) not ok")
	}
	if got := r(21); got != i18n.One {
		t.Errorf("uk(21) = %v, want One", got)
	}

	// A regional tag reduces to its language, unless CLDR gives the region a
	// rule of its own.
	for _, tag := range []string{"pt-BR", "pt_BR", "PT-br", "pt-AO"} {
		r, ok = cldr.PluralFor(tag)
		if !ok || r(0) != i18n.One {
			t.Errorf("PluralFor(%q) ok=%v, want the pt rule (0 is one)", tag, ok)
		}
	}
	for _, tag := range []string{"pt-PT", "pt_pt", "PT-pt"} {
		r, ok = cldr.PluralFor(tag)
		if !ok || r(0) != i18n.Other {
			t.Errorf("PluralFor(%q) ok=%v, want the pt-PT rule (0 is other)", tag, ok)
		}
	}

	// Unknown languages report false rather than guessing; the caller then
	// gets i18n's DefaultRule, which is honest about knowing nothing.
	for _, lang := range []string{
		"xx", "",
		// Deliberately excluded from scope.
		"ga", "cy", "mt",
	} {
		if _, ok := cldr.PluralFor(lang); ok {
			t.Errorf("PluralFor(%q) ok, want not ok", lang)
		}
	}
}

func TestAllIsComplete(t *testing.T) {
	want := []string{
		"en", "de", "nl", "sv", "da", "nb", "is",
		"fr", "es", "it", "pt", "pt-PT", "ro", "ca", "gl",
		"ru", "uk", "be", "pl", "cs", "sk", "sl", "hr", "sr", "bg", "mk",
		"lv", "lt", "fi", "et", "hu", "el", "sq", "tr", "eu",
		"ja", "zh", "ko", "th", "vi", "id", "ms", "hi", "ar", "he",
	}
	all := cldr.All()
	for _, lang := range want {
		if rule, ok := all[lang]; !ok {
			t.Errorf("missing rule for %s", lang)
		} else if rule == nil {
			t.Errorf("nil rule for %s", lang)
		}
	}
	if len(all) != len(want) {
		t.Errorf("All() has %d rules, want %d", len(all), len(want))
	}
}

func TestAllReturnsACopy(t *testing.T) {
	all := cldr.All()
	delete(all, "en")
	if _, ok := cldr.All()["en"]; !ok {
		t.Error("deleting from the returned map changed All()")
	}
}
