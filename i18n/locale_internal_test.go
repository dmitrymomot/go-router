package i18n

import (
	"strings"
	"testing"
)

func TestNormalizeTag(t *testing.T) {
	tests := map[string]string{
		"en":                             "en",
		"EN":                             "en",
		"en-us":                          "en-US",
		"en_US":                          "en-US",
		"UK-ua":                          "uk-UA",
		"pt-br":                          "pt-BR",
		"zh-cn":                          "zh-CN",
		" en-GB ":                        "en-GB",
		"fr-CA-x-ca":                     "fr-CA", // only the first two subtags are kept
		"zh-Hans-CN":                     "zh-HANS",
		"vi":                             "vi",    // a tag this package knows nothing about is still valid
		"ww-WW":                          "ww-WW", // ditto: no allowlist exists
		"":                               "",
		"-":                              "",
		"--":                             "",
		"-US":                            "", // empty language is not a tag
		"en-":                            "en",
		"en- ":                           "en", // a region of pure whitespace
		"_":                              "",
		strings.Repeat("a", maxTagLen+1): "", // oversized input is rejected before any work
	}
	for in, want := range tests {
		if got := normalizeTag(in); got != want {
			t.Errorf("normalizeTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeTagIsTotal(t *testing.T) {
	// Must never panic on adversarial input.
	for _, in := range []string{"\x00", "\xff\xfe", "en-\x00US", "---", "a-b-c-d-e", "\t\n", "EN_us_POSIX"} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("normalizeTag(%q) panicked: %v", in, r)
				}
			}()
			_ = normalizeTag(in)
		}()
	}
}

func TestLocale(t *testing.T) {
	l := newLocale("pt-br")
	if l.Tag() != "pt-BR" || l.Lang() != "pt" || l.String() != "pt-BR" || l.IsZero() {
		t.Errorf("newLocale(pt-br) = tag %q lang %q string %q zero %v", l.Tag(), l.Lang(), l.String(), l.IsZero())
	}

	// A tag with no region: Lang == Tag.
	l = newLocale("uk")
	if l.Tag() != "uk" || l.Lang() != "uk" {
		t.Errorf("newLocale(uk) = tag %q lang %q", l.Tag(), l.Lang())
	}

	// Unnormalizable input yields the zero Locale.
	if !newLocale("-").IsZero() {
		t.Error("newLocale(-) is not the zero Locale")
	}

	// The zero Locale is safe and empty.
	var zero Locale
	if !zero.IsZero() || zero.Tag() != "" || zero.Lang() != "" || zero.String() != "" {
		t.Errorf("zero Locale = tag %q lang %q string %q zero %v", zero.Tag(), zero.Lang(), zero.String(), zero.IsZero())
	}
}

func TestLocaleComparable(t *testing.T) {
	// Normalization makes equality meaningful across input spellings.
	if newLocale("en_US") != newLocale("EN-us") {
		t.Error("en_US and EN-us normalize to different Locales")
	}
	if newLocale("en") == newLocale("en-GB") {
		t.Error("en and en-GB are equal")
	}
}

func FuzzNormalizeTag(f *testing.F) {
	f.Add("en-US")
	f.Add("pt_br")
	f.Add("---")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		got := normalizeTag(s)
		// Normalization is idempotent: re-normalizing a normalized tag is a no-op.
		if got != "" {
			if again := normalizeTag(got); again != got {
				t.Errorf("normalizeTag(%q) = %q, but normalizing that again gives %q", s, got, again)
			}
		}
	})
}
