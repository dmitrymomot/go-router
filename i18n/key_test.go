package i18n_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router/i18n"
)

var (
	keyTitle = i18n.NewKey("app.title")
	keyItems = i18n.NewKey("cart.items")
	keyTypo  = i18n.NewKey("app.ttile")
)

func TestKey(t *testing.T) {
	if got := keyTitle.String(); got != "app.title" {
		t.Errorf("String() = %q, want app.title", got)
	}
	if keyTitle != i18n.NewKey("app.title") {
		t.Error("Key must be comparable by value")
	}
}

func TestTK(t *testing.T) {
	b := newBundle(t)
	en := b.Default()
	uk := b.ParseOrDefault("uk")

	if got, want := b.TK(en, keyTitle), "Dashboard"; got != want {
		t.Errorf("TK(en) = %q, want %q", got, want)
	}
	if got, want := b.TK(uk, keyTitle), "Панель"; got != want {
		t.Errorf("TK(uk) = %q, want %q", got, want)
	}
	// TK matches T exactly: same lookup, same fallback.
	if got, want := b.TK(uk, keyTitle), b.T(uk, "app.title"); got != want {
		t.Errorf("TK = %q, T = %q, want them equal", got, want)
	}
	// An unknown key echoes, like T.
	if got, want := b.TK(en, keyTypo), "app.ttile"; got != want {
		t.Errorf("TK(typo) = %q, want %q", got, want)
	}
}

func TestTNK(t *testing.T) {
	b := newBundle(t)
	en := b.Default()
	tests := []struct {
		n    int
		want string
	}{
		{0, "Your cart is empty"},
		{1, "1 item in your cart"},
		{7, "7 items in your cart"},
	}
	for _, tc := range tests {
		if got := b.TNK(en, keyItems, tc.n); got != tc.want {
			t.Errorf("TNK(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
	if got, want := b.TNK(en, keyItems, 7), b.TN(en, "cart.items", 7); got != want {
		t.Errorf("TNK = %q, TN = %q, want them equal", got, want)
	}
}

// TestTKPluralKeyRendersOtherForm pins that a Key ValidateKeys accepts as a
// plural (cart.items) does not echo when rendered with TK/T, which carry no
// count: it falls through to the plural's "other" form.
func TestTKPluralKeyRendersOtherForm(t *testing.T) {
	b := newBundle(t)
	en := b.Default()
	if err := b.ValidateKeys(keyItems); err != nil {
		t.Fatalf("ValidateKeys: %v", err)
	}
	got := b.TK(en, keyItems)
	if want := "{{count}} items in your cart"; got != want {
		t.Errorf("TK = %q, want %q", got, want)
	}
	if want := b.T(en, "cart.items"); got != want {
		t.Errorf("TK = %q, T = %q, want them equal", got, want)
	}
}

func TestValidateKeys(t *testing.T) {
	b := newBundle(t)

	// Plural keys validate too: cart.items is a plural entry, not a message.
	if err := b.ValidateKeys(keyTitle, keyItems); err != nil {
		t.Errorf("ValidateKeys(valid) = %v, want nil", err)
	}
	// No keys is not an error.
	if err := b.ValidateKeys(); err != nil {
		t.Errorf("ValidateKeys() = %v, want nil", err)
	}

	err := b.ValidateKeys(keyTitle, keyTypo)
	if !errors.Is(err, i18n.ErrUnknownKey) {
		t.Fatalf("ValidateKeys(typo) = %v, want ErrUnknownKey", err)
	}
	if !strings.Contains(err.Error(), "app.ttile") {
		t.Errorf("error %q must name the offending key", err)
	}
}

func TestValidateKeysChecksDefaultLocaleOnly(t *testing.T) {
	// app.only_en exists only in en (the default), so it validates even though
	// uk lacks it: every lookup falls through to the default anyway.
	b := newBundle(t)
	if err := b.ValidateKeys(i18n.NewKey("app.only_en")); err != nil {
		t.Errorf("ValidateKeys = %v, want nil", err)
	}
}

func TestValidateKeysReportsAllMissing(t *testing.T) {
	b := newBundle(t)
	err := b.ValidateKeys(i18n.NewKey("a.b"), keyTitle, i18n.NewKey("c.d"))
	if !errors.Is(err, i18n.ErrUnknownKey) {
		t.Fatalf("ValidateKeys = %v, want ErrUnknownKey", err)
	}
	msg := err.Error()
	for _, key := range []string{"a.b", "c.d"} {
		if !strings.Contains(msg, key) {
			t.Errorf("error %q does not name %q", msg, key)
		}
	}
	if strings.Contains(msg, "app.title") {
		t.Errorf("error %q names the valid key app.title", msg)
	}
}

func TestValidateKeysUsesConfiguredDefault(t *testing.T) {
	// With uk as the default, a key only en defines must fail validation.
	b, err := i18n.New("uk", i18n.WithMessages(testLocales()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := b.ValidateKeys(i18n.NewKey("app.only_en")); !errors.Is(err, i18n.ErrUnknownKey) {
		t.Errorf("ValidateKeys = %v, want ErrUnknownKey", err)
	}
}
