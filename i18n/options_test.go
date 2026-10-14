package i18n_test

import (
	"testing"

	"github.com/dmitrymomot/go-router/i18n"
)

func TestNewPanicsOnBadDefaultLocale(t *testing.T) {
	for _, tag := range []string{"", "-", "--", "-US"} {
		mustPanic(t, "not a language tag", func() {
			_, _ = i18n.New(tag, i18n.WithTranslations("en", "app", map[string]any{"k": "v"}))
		})
	}
}

func TestOptionsPanicOnBadArguments(t *testing.T) {
	rule := func(int) i18n.PluralCategory { return i18n.Other }
	tests := []struct {
		name string
		want string
		f    func()
	}{
		{"WithMessages nil fs", "needs a file system", func() { i18n.WithMessages(nil) }},
		{"WithTranslations bad tag", "not a language tag", func() { i18n.WithTranslations("-", "app", nil) }},
		{"WithTranslations empty tag", "not a language tag", func() { i18n.WithTranslations("", "app", nil) }},
		{"WithTranslations empty namespace", "needs a namespace", func() { i18n.WithTranslations("en", "", nil) }},
		{"WithPlural nil rule", "nil rule", func() { i18n.WithPlural("uk", nil) }},
		{"WithPlural bad tag", "not a language tag", func() { i18n.WithPlural("-", rule) }},
		{"WithPlural empty tag", "not a language tag", func() { i18n.WithPlural("", rule) }},
		{"WithFormat bad tag", "not a language tag", func() { i18n.WithFormat("-", i18n.Invariant) }},
		{"WithFormat empty tag", "not a language tag", func() { i18n.WithFormat("", i18n.Invariant) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mustPanic(t, tc.want, tc.f)
		})
	}
}

func TestOptionsAcceptValidArguments(t *testing.T) {
	// A nil data map is a valid (empty) namespace.
	b, err := i18n.New("en",
		i18n.WithTranslations("en", "app", map[string]any{"k": "v"}),
		i18n.WithTranslations("de", "empty", nil),
		i18n.WithPlural("pt-BR", i18n.DefaultRule),
		i18n.WithFormat("en_gb", i18n.Invariant),
		i18n.WithMissingHandler(nil),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := len(b.Locales()); got != 2 {
		t.Errorf("Locales() has %d entries, want 2", got)
	}
}
