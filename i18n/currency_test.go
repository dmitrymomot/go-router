package i18n_test

import (
	"math"
	"testing"

	"github.com/dmitrymomot/go-router/i18n"
)

func usd(minor int64) i18n.Money { return i18n.Money{Symbol: "$", Minor: minor, Digits: 2} }
func eur(minor int64) i18n.Money { return i18n.Money{Symbol: "€", Minor: minor, Digits: 2} }
func jpy(minor int64) i18n.Money { return i18n.Money{Symbol: "¥", Minor: minor, Digits: 0} }
func bhd(minor int64) i18n.Money { return i18n.Money{Symbol: ".د.ب", Minor: minor, Digits: 3} }

// curBundle wires "de" to deSpec (declared in format_test.go): symbol after
// the amount with a space, German separators. "en" gets Invariant: symbol
// before, no space.
func curBundle(t *testing.T) *i18n.Bundle {
	t.Helper()
	return newBundle(t, i18n.WithFormat("de", deSpec))
}

func TestCurrency(t *testing.T) {
	b := curBundle(t)
	en, de := b.Default(), b.ParseOrDefault("de")
	var zeroLoc i18n.Locale

	tests := []struct {
		name string
		loc  i18n.Locale
		m    i18n.Money
		want string
	}{
		{"invariant", en, usd(123456), "$1,234.56"},
		{"invariant zero", en, usd(0), "$0.00"},
		{"invariant padded", en, usd(5), "$0.05"},
		{"invariant negative", en, usd(-123456), "-$1,234.56"},
		// Symbol after the amount, separated by a space; German separators.
		{"wired spec", de, eur(123456), "1.234,56\u00a0€"},
		{"wired spec negative", de, eur(-123456), "-1.234,56\u00a0€"},
		// The locale contributes separators and placement; money contributes
		// the symbol and the digits. A German reader viewing USD is correct.
		{"multi currency", de, usd(123456), "1.234,56\u00a0$"},
		// JPY has no minor units: no decimal separator at all.
		{"no minor digits", en, jpy(1235), "¥1,235"},
		{"three minor digits", en, bhd(1234), ".د.ب1.234"},
		// Padding: 5 minor units of BHD is 0.005.
		{"three minor digits padded", en, bhd(5), ".د.ب0.005"},
		{"zero locale", zeroLoc, usd(100), "$1.00"},
		// The zero Money has no symbol and no digits; rendering must not panic.
		{"zero money", en, i18n.Money{}, "0"},
		// A negative Digits is malformed and renders like zero digits.
		{"negative digits", en, i18n.Money{Symbol: "X", Minor: 12345, Digits: -2}, "X12,345"},
		// MinInt64 pins the signed-magnitude conversion: a plain negation overflows.
		{"min int64", en, i18n.Money{Symbol: "X", Minor: math.MinInt64}, "-X9,223,372,036,854,775,808"},
		{"max int64", en, i18n.Money{Symbol: "X", Minor: math.MaxInt64, Digits: 2}, "X92,233,720,368,547,758.07"},
	}
	for _, tc := range tests {
		if got := b.Currency(tc.loc, tc.m); got != tc.want {
			t.Errorf("%s: Currency(%+v) = %q, want %q", tc.name, tc.m, got, tc.want)
		}
	}
}

// TestCurrencyLargeDigitsNeverPanics pins totality against a pathologically
// large Digits: naively computing 10^digits in a uint64 would silently wrap
// (and can wrap to exactly zero), turning the following division into a
// divide-by-zero panic. Digits is clamped to 19 instead.
func TestCurrencyLargeDigitsNeverPanics(t *testing.T) {
	b := curBundle(t)
	for _, digits := range []int{19, 20, 64, 1000, math.MaxInt} {
		got := b.Currency(b.Default(), i18n.Money{Symbol: "Y", Minor: 123, Digits: digits})
		if want := "Y0." + "0000000000000000123"; got != want {
			t.Errorf("Digits %d: Currency = %q, want %q", digits, got, want)
		}
	}
}

func TestAppendCurrency(t *testing.T) {
	b := curBundle(t)
	if got, want := string(b.AppendCurrency([]byte("x:"), b.Default(), usd(100))), "x:$1.00"; got != want {
		t.Errorf("AppendCurrency = %q, want %q", got, want)
	}
}

func TestAppendCurrencyZeroAlloc(t *testing.T) {
	b := curBundle(t)
	en := b.Default()
	m := usd(123456)
	dst := make([]byte, 0, 64)
	if allocs := testing.AllocsPerRun(200, func() { _ = b.AppendCurrency(dst[:0], en, m) }); allocs != 0 {
		t.Errorf("AppendCurrency into a sized buffer allocated %v times, want 0", allocs)
	}
}
