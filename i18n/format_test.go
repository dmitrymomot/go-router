package i18n_test

import (
	"math"
	"testing"

	"github.com/dmitrymomot/go-router/i18n"
)

// deSpec is a German-style spec, declared here in the test: this package ships
// no per-locale data, so a formatting test must bring its own.
var deSpec = i18n.FormatSpec{
	DecimalSep:     ",",
	GroupSep:       ".",
	DateLayout:     "02.01.2006",
	TimeLayout:     "15:04",
	DateTimeLayout: "02.01.2006 15:04",
	CurrencySpace:  true,
	PercentSpace:   true,
}

// frSpec uses U+00A0 NO-BREAK SPACE as its group separator, written as an
// escape so it is visible.
var frSpec = i18n.FormatSpec{
	DecimalSep:     ",",
	GroupSep:       " ",
	DateLayout:     "02/01/2006",
	TimeLayout:     "15:04",
	DateTimeLayout: "02/01/2006 15:04",
	CurrencySpace:  true,
	PercentSpace:   true,
}

func fmtBundle(t *testing.T) *i18n.Bundle {
	t.Helper()
	return newBundle(t,
		i18n.WithFormat("de", deSpec),
		i18n.WithFormat("vi", frSpec), // arbitrary: proves specs are just data
	)
}

func TestNumberInvariantDefault(t *testing.T) {
	b := fmtBundle(t)
	// uk has no wired spec, so it formats with Invariant, not with anything
	// this package "knows" about Ukrainian, because it knows nothing.
	uk := b.ParseOrDefault("uk")
	if got, want := b.Number(uk, 1234567.89), "1,234,567.89"; got != want {
		t.Errorf("Number = %q, want %q", got, want)
	}
	if got, want := b.Number(uk, 0), "0"; got != want {
		t.Errorf("Number(0) = %q, want %q", got, want)
	}
}

func TestNumberWiredSpec(t *testing.T) {
	b := fmtBundle(t)
	de := b.ParseOrDefault("de")
	vi := b.ParseOrDefault("vi")
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"de", b.Number(de, 1234567.89), "1.234.567,89"},
		{"de negative", b.Number(de, -1234.5), "-1.234,5"},
		{"vi", b.Number(vi, 1234567.89), "1 234 567,89"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s: Number = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestNumberGrouping(t *testing.T) {
	b := fmtBundle(t)
	en := b.Default()
	tests := map[float64]string{
		0:       "0",
		1:       "1",
		12:      "12",
		123:     "123",
		1234:    "1,234",
		12345:   "12,345",
		123456:  "123,456",
		1234567: "1,234,567",
		-1234:   "-1,234",
		-123:    "-123",
		1000:    "1,000",
		1000000: "1,000,000",
	}
	for in, want := range tests {
		if got := b.Number(en, in); got != want {
			t.Errorf("Number(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestNumberFractions(t *testing.T) {
	b := fmtBundle(t)
	en := b.Default()
	// Shortest round-tripping representation; Number does not round.
	tests := map[float64]string{
		0.1:       "0.1",
		1.5:       "1.5",
		1234.5678: "1,234.5678",
		-0.5:      "-0.5",
	}
	for in, want := range tests {
		if got := b.Number(en, in); got != want {
			t.Errorf("Number(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestNumberInt(t *testing.T) {
	b := fmtBundle(t)
	if got, want := b.NumberInt(b.Default(), 1234567), "1,234,567"; got != want {
		t.Errorf("NumberInt = %q, want %q", got, want)
	}
	if got, want := b.NumberInt(b.ParseOrDefault("de"), -1234567), "-1.234.567"; got != want {
		t.Errorf("NumberInt(de) = %q, want %q", got, want)
	}
	if got, want := b.NumberInt(b.Default(), 0), "0"; got != want {
		t.Errorf("NumberInt(0) = %q, want %q", got, want)
	}
}

// TestNumberIntMinInt64 pins the signed-magnitude conversion: a plain
// negation of math.MinInt64 overflows int64, since its magnitude has no
// int64 representation. The conversion must go through uint64 instead.
func TestNumberIntMinInt64(t *testing.T) {
	b := fmtBundle(t)
	if got, want := b.NumberInt(b.Default(), math.MinInt64), "-9,223,372,036,854,775,808"; got != want {
		t.Errorf("NumberInt(MinInt64) = %q, want %q", got, want)
	}
}

// TestNumberIntLongGroupSepDoesNotPanic pins appendGroupedUint's total
// contract: GroupSep is a public FormatSpec field an app can set to any
// string, and rendering must never panic regardless of its length. An 8-byte
// separator combined with a 19-digit int64 (6 group separators) needs 19 +
// 6*8 = 67 bytes, more than a fixed 64-byte stack buffer holds.
func TestNumberIntLongGroupSepDoesNotPanic(t *testing.T) {
	b := newBundle(t, i18n.WithFormat("en", i18n.FormatSpec{DecimalSep: ".", GroupSep: "GROUPSEP"}))
	got := b.NumberInt(b.Default(), math.MaxInt64)
	if want := "9GROUPSEP223GROUPSEP372GROUPSEP036GROUPSEP854GROUPSEP775GROUPSEP807"; got != want {
		t.Errorf("NumberInt(MaxInt64) = %q, want %q", got, want)
	}
}

func TestEmptyGroupSepDisablesGrouping(t *testing.T) {
	b := newBundle(t, i18n.WithFormat("en", i18n.FormatSpec{DecimalSep: ".", GroupSep: ""}))
	en := b.Default()
	if got, want := b.Number(en, 1234567.5), "1234567.5"; got != want {
		t.Errorf("Number = %q, want %q", got, want)
	}
	if got, want := b.NumberInt(en, 1234567), "1234567"; got != want {
		t.Errorf("NumberInt = %q, want %q", got, want)
	}
}

func TestPercent(t *testing.T) {
	b := fmtBundle(t)
	en, de := b.Default(), b.ParseOrDefault("de")
	tests := []struct {
		name  string
		loc   i18n.Locale
		ratio float64
		want  string
	}{
		{"half", en, 0.5, "50%"},
		{"fraction", en, 0.125, "12.5%"},
		{"zero", en, 0, "0%"},
		{"one", en, 1, "100%"},
		// PercentSpace inserts a space before the sign.
		{"spaced", de, 0.5, "50\u00a0%"},
		{"spaced negative", de, -0.075, "-7,5\u00a0%"},
		// ratio*100 in float64 is a near miss (0.29*100 == 28.999999999999996);
		// rounding the scaled value collapses the artifact instead of printing it.
		{"0.29", en, 0.29, "29%"},
		{"0.07", en, 0.07, "7%"},
		{"0.1", en, 0.1, "10%"},
		{"0.815", en, 0.815, "81.5%"},
	}
	for _, tc := range tests {
		if got := b.Percent(tc.loc, tc.ratio); got != tc.want {
			t.Errorf("%s: Percent(%v) = %q, want %q", tc.name, tc.ratio, got, tc.want)
		}
	}
}

func TestNumberZeroLocaleUsesInvariant(t *testing.T) {
	b := fmtBundle(t)
	var zero i18n.Locale
	// The zero Locale resolves to the default (en), which has no wired spec.
	if got, want := b.Number(zero, 1234.5), "1,234.5"; got != want {
		t.Errorf("Number = %q, want %q", got, want)
	}
}

var (
	gbSpec = i18n.FormatSpec{DecimalSep: ".", GroupSep: " ", DateLayout: "02/01/2006", TimeLayout: "15:04", DateTimeLayout: "02/01/2006 15:04"}
	enSpec = i18n.FormatSpec{DecimalSep: ",", GroupSep: ".", DateLayout: "2006-01-02", TimeLayout: "15:04", DateTimeLayout: "2006-01-02 15:04"}
)

// TestFormatSpecTagBeatsLang covers the first rung of the resolution order:
// an exact-tag spec must be preferred over one wired for the base language.
func TestFormatSpecTagBeatsLang(t *testing.T) {
	b := newBundle(t, i18n.WithFormat("en-GB", gbSpec))
	// en-GB has its own spec...
	if got, want := b.Number(b.ParseOrDefault("en-GB"), 1234.5), "1 234.5"; got != want {
		t.Errorf("Number(en-GB) = %q, want %q", got, want)
	}
	// ...while en still gets Invariant: an exact-tag spec must not leak to a
	// sibling locale that merely shares the base language.
	if got, want := b.Number(b.Default(), 1234.5), "1,234.5"; got != want {
		t.Errorf("Number(en) = %q, want %q", got, want)
	}
}

// TestFormatSpecTagPriorityOverLang pins the actual priority order, which
// neither TestFormatSpecTagBeatsLang nor TestFormatSpecLangAppliesToRegion
// forces: here en-GB and its base language en are both wired, with specs that
// disagree, and resolution must pick en-GB's own tag spec.
func TestFormatSpecTagPriorityOverLang(t *testing.T) {
	b := newBundle(t, i18n.WithFormat("en-GB", gbSpec), i18n.WithFormat("en", enSpec))
	if got, want := b.Number(b.ParseOrDefault("en-GB"), 1234.5), "1 234.5"; got != want {
		t.Errorf("Number(en-GB) = %q, want %q", got, want)
	}
	// en is its own base language, so it uses its own wired spec.
	if got, want := b.Number(b.Default(), 1234.5), "1.234,5"; got != want {
		t.Errorf("Number(en) = %q, want %q", got, want)
	}
}

// TestFormatSpecLangAppliesToRegion covers the second rung: a spec wired for
// the base language covers a regional variant with no spec of its own.
func TestFormatSpecLangAppliesToRegion(t *testing.T) {
	b := newBundle(t, i18n.WithFormat("en", enSpec))
	if got, want := b.Number(b.ParseOrDefault("en-GB"), 1234.5), "1.234,5"; got != want {
		t.Errorf("Number(en-GB) = %q, want %q", got, want)
	}
}

// TestFormatNeverFallsBackToDefaultLocaleSpec is the load-bearing rule from
// resolve's doc comment. The bundle's default locale (en) gets an
// explicit, non-Invariant spec; a different, supported-but-unconfigured locale
// (uk) must still render with Invariant, not silently inherit en's spec
// through its default-locale status.
func TestFormatNeverFallsBackToDefaultLocaleSpec(t *testing.T) {
	b := newBundle(t, i18n.WithFormat("en", i18n.FormatSpec{
		DecimalSep: "X", GroupSep: "Y",
		DateLayout: "2006", TimeLayout: "15:04", DateTimeLayout: "2006 15:04",
	}))
	// Sanity check: en itself really did pick up the wired spec.
	if got, want := b.Number(b.Default(), 1234.5), "1Y234X5"; got != want {
		t.Fatalf("Number(en) = %q, want %q", got, want)
	}
	uk := b.ParseOrDefault("uk")
	if uk.Tag() != "uk" {
		t.Fatalf("uk resolved to %q, want a supported non-default locale", uk.Tag())
	}
	// If uk's spec fell back to the default locale's wired spec instead of
	// Invariant, this would render "1Y234X5" too.
	if got, want := b.Number(uk, 1234.5), "1,234.5"; got != want {
		t.Errorf("Number(uk) = %q, want %q", got, want)
	}
}

func TestNumberNaNAndInf(t *testing.T) {
	b := fmtBundle(t)
	en := b.Default()
	// Rendering is total: NaN and infinities render as themselves rather than
	// being fed through the digit-grouping logic.
	tests := []struct {
		in   float64
		want string
	}{
		{math.NaN(), "NaN"},
		{math.Inf(1), "+Inf"},
		{math.Inf(-1), "-Inf"},
	}
	for _, tc := range tests {
		if got := b.Number(en, tc.in); got != tc.want {
			t.Errorf("Number(%v) = %q, want %q", tc.in, got, tc.want)
		}
		if got := b.Percent(en, tc.in); got != tc.want+"%" {
			t.Errorf("Percent(%v) = %q, want %q", tc.in, got, tc.want+"%")
		}
	}
}

func TestAppendNumberAppends(t *testing.T) {
	b := fmtBundle(t)
	en := b.Default()
	if got, want := string(b.AppendNumber([]byte("x:"), en, 1234)), "x:1,234"; got != want {
		t.Errorf("AppendNumber = %q, want %q", got, want)
	}
	if got, want := string(b.AppendNumberInt([]byte("x:"), en, 1234)), "x:1,234"; got != want {
		t.Errorf("AppendNumberInt = %q, want %q", got, want)
	}
	if got, want := string(b.AppendPercent([]byte("x:"), en, 0.5)), "x:50%"; got != want {
		t.Errorf("AppendPercent = %q, want %q", got, want)
	}
}

func TestAppendNumberZeroAlloc(t *testing.T) {
	b := fmtBundle(t)
	en := b.Default()
	dst := make([]byte, 0, 64)
	tests := []struct {
		name string
		f    func()
	}{
		{"AppendNumber", func() { _ = b.AppendNumber(dst[:0], en, 1234567.89) }},
		{"AppendNumberInt", func() { _ = b.AppendNumberInt(dst[:0], en, 1234567) }},
		{"AppendPercent", func() { _ = b.AppendPercent(dst[:0], en, 0.5) }},
	}
	for _, tc := range tests {
		if allocs := testing.AllocsPerRun(200, tc.f); allocs != 0 {
			t.Errorf("%s into a sized buffer allocated %v times, want 0", tc.name, allocs)
		}
	}
}

func TestMinGroupingDigits(t *testing.T) {
	es := deSpec
	es.MinGroupingDigits = 2
	b := newBundle(t, i18n.WithFormat("de", es))
	de := b.ParseOrDefault("de")
	for _, tc := range []struct{ name, got, want string }{
		{"Number 4 digits", b.Number(de, 1234.5), "1234,5"},
		{"Number 5 digits", b.Number(de, 12345.5), "12.345,5"},
		{"Number 7 digits", b.Number(de, -1234567), "-1.234.567"},
		{"NumberInt 4 digits", b.NumberInt(de, -1234), "-1234"},
		{"NumberInt 5 digits", b.NumberInt(de, 12345), "12.345"},
		{"NumberInt 3 digits", b.NumberInt(de, 999), "999"},
		{"Currency 4 digits", b.Currency(de, i18n.Money{Minor: 123400, Digits: 2, Symbol: "€"}), "1234,00\u00a0€"},
		{"Currency 5 digits", b.Currency(de, i18n.Money{Minor: 1234500, Digits: 2, Symbol: "€"}), "12.345,00\u00a0€"},
		{"Currency no minor", b.Currency(de, i18n.Money{Minor: 1234, Symbol: "¥"}), "1234\u00a0¥"},
		{"Percent", b.Percent(de, 12.34), "1234\u00a0%"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}

	// Zero and one both group from four digits.
	for _, mgd := range []int{0, 1} {
		s := deSpec
		s.MinGroupingDigits = mgd
		b := newBundle(t, i18n.WithFormat("de", s))
		if got := b.NumberInt(b.ParseOrDefault("de"), 1234); got != "1.234" {
			t.Errorf("MinGroupingDigits %d: NumberInt(1234) = %q, want 1.234", mgd, got)
		}
	}
}

func TestPercentBefore(t *testing.T) {
	tr := deSpec
	tr.PercentSpace = false
	tr.PercentBefore = true
	eu := tr
	eu.PercentSpace = true
	b := newBundle(t, i18n.WithFormat("de", tr), i18n.WithFormat("vi", eu))
	de, vi := b.ParseOrDefault("de"), b.ParseOrDefault("vi")
	for _, tc := range []struct{ name, got, want string }{
		{"tr", b.Percent(de, 0.5), "%50"},
		{"tr negative", b.Percent(de, -0.125), "-%12,5"},
		{"tr negative zero", b.Percent(de, -1e-10), "-%0"},
		{"trailing negative zero", b.Percent(b.ParseOrDefault("en"), -1e-10), "-0%"},
		{"tr grouped", b.Percent(de, 12.5), "%1.250"},
		{"eu", b.Percent(vi, 0.5), "%\u00a050"},
		{"NaN", b.Percent(de, math.NaN()), "%NaN"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}
