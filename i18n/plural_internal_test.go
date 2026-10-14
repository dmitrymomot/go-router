package i18n

import (
	"math"
	"slices"
	"testing"
)

func TestFormFallback(t *testing.T) {
	tests := []struct {
		from PluralCategory
		want []PluralCategory
	}{
		{Zero, []PluralCategory{Other}},
		{One, []PluralCategory{Other}},
		{Two, []PluralCategory{Few, Many, Other}},
		{Few, []PluralCategory{Many, Other}},
		{Many, []PluralCategory{Other}},
		{Other, nil},
		{PluralCategory(200), nil},
	}
	for _, tc := range tests {
		if got := formFallback(tc.from); !slices.Equal(got, tc.want) {
			t.Errorf("formFallback(%v) = %v, want %v", tc.from, got, tc.want)
		}
	}
}

func TestPluralCategoryString(t *testing.T) {
	want := map[PluralCategory]string{Zero: "zero", One: "one", Two: "two", Few: "few", Many: "many", Other: "other"}
	for c, s := range want {
		if got := c.String(); got != s {
			t.Errorf("PluralCategory(%d).String() = %q, want %q", uint8(c), got, s)
		}
	}
	// Out-of-range degrades to the terminal form rather than panicking.
	if got := PluralCategory(200).String(); got != "other" {
		t.Errorf("PluralCategory(200).String() = %q, want other", got)
	}
}

func TestCategoryByName(t *testing.T) {
	for name, want := range map[string]PluralCategory{
		"zero": Zero, "one": One, "two": Two, "few": Few, "many": Many, "other": Other,
	} {
		got, ok := categoryByName(name)
		if !ok || got != want {
			t.Errorf("categoryByName(%q) = %v, %v, want %v, true", name, got, ok, want)
		}
	}
	for _, name := range []string{"", "save", "One", "ZERO", "otherx"} {
		if _, ok := categoryByName(name); ok {
			t.Errorf("categoryByName(%q) resolved, want not ok", name)
		}
	}
}

func TestDefaultRule(t *testing.T) {
	tests := []struct {
		n    int
		want PluralCategory
	}{
		{0, Zero},
		{1, One},
		{-1, One},
		{2, Many},
		{5, Many},
		{-7, Many},
		{100, Many},
		{math.MaxInt, Many},
		{math.MinInt, Many}, // total: no abs() overflow path
	}
	for _, tc := range tests {
		if got := DefaultRule(tc.n); got != tc.want {
			t.Errorf("DefaultRule(%d) = %v, want %v", tc.n, got, tc.want)
		}
	}
}

func TestSupportedForms(t *testing.T) {
	oneOther := func(n int) PluralCategory {
		if n == 1 || n == -1 {
			return One
		}
		return Other
	}
	if got, want := SupportedForms(oneOther), []PluralCategory{One, Other}; !slices.Equal(got, want) {
		t.Errorf("SupportedForms(oneOther) = %v, want %v", got, want)
	}

	// The built-in default produces exactly zero/one/many.
	if got, want := SupportedForms(DefaultRule), []PluralCategory{Zero, One, Many}; !slices.Equal(got, want) {
		t.Errorf("SupportedForms(DefaultRule) = %v, want %v", got, want)
	}

	always := func(int) PluralCategory { return Other }
	if got, want := SupportedForms(always), []PluralCategory{Other}; !slices.Equal(got, want) {
		t.Errorf("SupportedForms(always-other) = %v, want %v", got, want)
	}

	// Results are in canonical category order regardless of probe order, and
	// an out-of-range category is ignored.
	reversed := func(n int) PluralCategory {
		switch n {
		case 0:
			return Other
		case 1:
			return One
		case 2:
			return PluralCategory(200)
		}
		return Zero
	}
	if got, want := SupportedForms(reversed), []PluralCategory{Zero, One, Other}; !slices.Equal(got, want) {
		t.Errorf("SupportedForms(reversed) = %v, want %v", got, want)
	}
}
