package cldr_test

import (
	"testing"

	"github.com/dmitrymomot/go-router/i18n"
	"github.com/dmitrymomot/go-router/i18n/cldr"
)

func TestFormatSpecsAreComplete(t *testing.T) {
	// TestFormatsMatchCLDRGolden pins the set and the values; this checks the
	// invariants every spec must hold whatever CLDR says.
	for tag, s := range cldr.AllFormats() {
		for field, v := range map[string]string{
			"DecimalSep": s.DecimalSep, "GroupSep": s.GroupSep, "DateLayout": s.DateLayout,
			"TimeLayout": s.TimeLayout, "DateTimeLayout": s.DateTimeLayout,
		} {
			if v == "" {
				t.Errorf("%s: empty %s", tag, field)
			}
		}
		if s.DecimalSep == s.GroupSep {
			t.Errorf("%s: separators must differ, both are %q", tag, s.DecimalSep)
		}
		// The grouping engine inserts GroupSep between every 3-digit group;
		// every real CLDR separator is 1-2 bytes. This is cheap, alongside
		// the render tests below, not instead of them.
		if len(s.GroupSep) > 4 {
			t.Errorf("%s: GroupSep too long (%d bytes)", tag, len(s.GroupSep))
		}
	}
}

func TestAllFormatsReturnsACopy(t *testing.T) {
	all := cldr.AllFormats()
	delete(all, "en-US")
	if _, ok := cldr.AllFormats()["en-US"]; !ok {
		t.Error("deleting from the returned map changed AllFormats()")
	}
}

func TestFormatForUnknown(t *testing.T) {
	for _, tag := range []string{
		"ww-WW", "",
		// Specs are per-tag: a bare language has no spec.
		"es",
	} {
		if _, ok := cldr.FormatFor(tag); ok {
			t.Errorf("FormatFor(%q) ok, want not ok", tag)
		}
	}
}

// TestFormatForNormalizesInput exercises the underscore and case handling
// FormatFor does on its own, independent of i18n's tag normalization.
func TestFormatForNormalizesInput(t *testing.T) {
	want, ok := cldr.FormatFor("es-MX")
	if !ok {
		t.Fatal("no spec for es-MX")
	}
	for _, in := range []string{"es_mx", "ES-mx", " es-MX "} {
		got, ok := cldr.FormatFor(in)
		if !ok || got != want {
			t.Errorf("FormatFor(%q) = %+v, %v, want the es-MX spec", in, got, ok)
		}
	}
}

// TestFormatsRenderThroughBundle proves the specs are usable as plain data:
// wire one and the engines in package i18n do the rest.
func TestFormatsRenderThroughBundle(t *testing.T) {
	b, err := i18n.New("en",
		i18n.WithTranslations("en", "app", map[string]any{"x": "y"}),
		i18n.WithFormat("en", cldr.FormatDeDE), // arbitrary pairing: it is just data
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := b.Number(b.Default(), 1234567.89), "1.234.567,89"; got != want {
		t.Errorf("Number = %q, want %q", got, want)
	}
}
