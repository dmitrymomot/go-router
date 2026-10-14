package i18n_test

import (
	"testing"
	"time"

	"github.com/dmitrymomot/go-router/i18n"
)

func TestWithLocaleOutsideARequest(t *testing.T) {
	b := webBundle(t)
	ctx := b.WithLocale(t.Context(), b.ParseOrDefault("uk"))

	if got := i18n.T(ctx, "app.hi"); got != "Привіт" {
		t.Errorf("T = %q, want %q", got, "Привіт")
	}
	if got := i18n.LocaleFrom(ctx).Tag(); got != "uk" {
		t.Errorf("LocaleFrom = %q, want uk", got)
	}
	if got := b.Ctx(ctx).Locale().Tag(); got != "uk" {
		t.Errorf("Ctx = %q, want uk", got)
	}
}

func TestContextWithoutLocalizer(t *testing.T) {
	b := webBundle(t)
	ctx := t.Context()

	if !i18n.From(ctx).IsZero() {
		t.Error("From of a bare context is bound")
	}
	if got := b.Ctx(ctx).Locale().Tag(); got != "en" {
		t.Errorf("Ctx = %q, want the default en", got)
	}
	if !i18n.LocaleFrom(ctx).IsZero() {
		t.Error("LocaleFrom of a bare context is not zero")
	}
}

func TestContextFunctions(t *testing.T) {
	b := webBundle(t)
	k := i18n.NewKey("app.items")
	at := time.Date(2026, 10, 14, 9, 5, 0, 0, time.UTC)

	for _, tc := range []struct {
		name, got, want string
	}{
		{"T", i18n.T(t.Context(), "app.hi"), "app.hi"},
		{"TN", i18n.TN(t.Context(), "app.items", 2), "app.items"},
	} {
		if tc.got != tc.want {
			t.Errorf("bare %s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}

	ctx := b.WithLocale(t.Context(), b.Default())
	for _, tc := range []struct {
		name, got, want string
	}{
		{"T", i18n.T(ctx, "app.hi"), "Hello"},
		{"TN", i18n.TN(ctx, "app.items", 1), "1 item"},
		{"TK", i18n.TK(ctx, k), "{{count}} items"},
		{"TNK", i18n.TNK(ctx, k, 2), "2 items"},
		{"Number", i18n.Number(ctx, 1234.5), "1,234.5"},
		{"NumberInt", i18n.NumberInt(ctx, -1234567), "-1,234,567"},
		{"Percent", i18n.Percent(ctx, 0.25), "25%"},
		{"Currency", i18n.Currency(ctx, i18n.Money{Minor: 123456, Digits: 2, Symbol: "$"}), "$1,234.56"},
		{"Date", i18n.Date(ctx, at), "2026-10-14"},
		{"Time", i18n.Time(ctx, at), "09:05"},
		{"DateTime", i18n.DateTime(ctx, at), "2026-10-14 09:05"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}
