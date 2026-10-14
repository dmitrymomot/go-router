package cldr_test

import (
	"testing"
	"time"

	"github.com/dmitrymomot/go-router/i18n"
	"github.com/dmitrymomot/go-router/i18n/cldr"
)

// wantLocales is the full set of locale tags this package must carry. It
// pins the count so a var that is written but never added to allFormats (or
// vice versa) is caught.
var wantLocales = []string{
	"en-US", "en-GB",
	"de-DE", "fr-FR",
	"es-ES", "es-MX", "es-AR", "es-CO", "es-CL",
	"it-IT",
	"pt-BR", "pt-PT",
	"nl-NL",
	"pl-PL", "cs-CZ", "uk-UA", "ru-RU",
	"tr-TR",
	"ar-AE",
	"ja-JP", "zh-CN", "ko-KR",
}

func TestFormatSpecsAreComplete(t *testing.T) {
	all := cldr.AllFormats()
	if len(all) != len(wantLocales) {
		t.Errorf("AllFormats() has %d specs, want %d: the set drifted from the locale list this package promises", len(all), len(wantLocales))
	}
	for _, tag := range wantLocales {
		s, ok := all[tag]
		if !ok {
			t.Errorf("%s: missing from AllFormats", tag)
			continue
		}
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

// TestNBSPGroupSeparators pins the locales whose CLDR group separator is
// U+00A0. A future edit "fixing" one to a plain space is a regression, and an
// invisible character is exactly the kind of thing a reviewer cannot see.
func TestNBSPGroupSeparators(t *testing.T) {
	for _, tag := range []string{"fr-FR", "pl-PL", "cs-CZ", "uk-UA", "ru-RU", "pt-PT"} {
		s, ok := cldr.FormatFor(tag)
		if !ok {
			t.Errorf("no spec for %s", tag)
			continue
		}
		if s.GroupSep != " " {
			t.Errorf("%s GroupSep = %q, want U+00A0 NO-BREAK SPACE", tag, s.GroupSep)
		}
	}
	// And the ones that are genuinely a plain separator.
	for tag, want := range map[string]string{
		"en-US": ",",
		"de-DE": ".",
		"pt-BR": ".", // pt-BR keeps the pt-root period group, unlike pt-PT's NBSP override
	} {
		s, _ := cldr.FormatFor(tag)
		if s.GroupSep != want {
			t.Errorf("%s GroupSep = %q, want %q", tag, s.GroupSep, want)
		}
	}
}

func TestLatAmFormats(t *testing.T) {
	// LatAm is a formats story, not a plurals one: es-MX and es-ES pluralize
	// identically but format nothing alike.
	mx, ok := cldr.FormatFor("es-MX")
	if !ok {
		t.Fatal("no spec for es-MX")
	}
	if mx.DecimalSep != "." || mx.GroupSep != "," || !mx.CurrencyBefore || mx.CurrencySpace {
		t.Errorf("es-MX = %+v", mx)
	}

	es, ok := cldr.FormatFor("es-ES")
	if !ok {
		t.Fatal("no spec for es-ES")
	}
	if es.DecimalSep != "," || es.GroupSep != "." || es.CurrencyBefore {
		t.Errorf("es-ES = %+v", es)
	}

	// es-AR/es-CO share es-ES's separators but not its currency placement or
	// clock: symbol-first with a space, 12-hour.
	for _, tag := range []string{"es-AR", "es-CO"} {
		s, ok := cldr.FormatFor(tag)
		if !ok {
			t.Errorf("no spec for %s", tag)
			continue
		}
		if s.DecimalSep != "," || s.GroupSep != "." || !s.CurrencyBefore || !s.CurrencySpace || s.TimeLayout != "3:04 PM" {
			t.Errorf("%s = %+v", tag, s)
		}
	}

	// es-CL is the odd one out: hyphenated date, no currency space.
	cl, ok := cldr.FormatFor("es-CL")
	if !ok {
		t.Fatal("no spec for es-CL")
	}
	if cl.DateLayout != "02-01-2006" || !cl.CurrencyBefore || cl.CurrencySpace {
		t.Errorf("es-CL = %+v", cl)
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

func TestFormatsRenderNBSP(t *testing.T) {
	b, err := i18n.New("fr",
		i18n.WithTranslations("fr", "app", map[string]any{"x": "y"}),
		i18n.WithFormat("fr", cldr.FormatFrFR),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The separator here is U+00A0, written as an escape so it is visible.
	if got, want := b.Number(b.Default(), 1234567.89), "1 234 567,89"; got != want {
		t.Errorf("Number = %q, want %q", got, want)
	}
}

// renderCase is one locale's expected rendering across every engine that
// consumes a FormatSpec. Expected strings are written from CLDR/locale
// knowledge, independent of the spec's own field values, so a transposed
// separator or a wrong currency placement in format.go fails the check
// rather than restating it.
type renderCase struct {
	tag      string
	spec     i18n.FormatSpec
	number   string // 1234567.89
	currency string // 1234.56, symbol "$"
	percent  string // 0.5
	date     string // reference time 2006-01-02
	clock    string // reference time 15:04:05
	dateTime string
}

var renderCases = []renderCase{
	{"en-US", cldr.FormatEnUS, "1,234,567.89", "$1,234.56", "50%", "1/2/2006", "3:04 PM", "1/2/2006 3:04 PM"},
	{"en-GB", cldr.FormatEnGB, "1,234,567.89", "$1,234.56", "50%", "02/01/2006", "15:04", "02/01/2006 15:04"},
	{"de-DE", cldr.FormatDeDE, "1.234.567,89", "1.234,56 $", "50 %", "02.01.2006", "15:04", "02.01.2006 15:04"},
	{"fr-FR", cldr.FormatFrFR, "1 234 567,89", "1 234,56 $", "50 %", "02/01/2006", "15:04", "02/01/2006 15:04"},
	{"es-ES", cldr.FormatEsES, "1.234.567,89", "1.234,56 $", "50 %", "2/1/2006", "15:04", "2/1/2006 15:04"},
	{"es-MX", cldr.FormatEsMX, "1,234,567.89", "$1,234.56", "50%", "02/01/2006", "3:04 PM", "02/01/2006 3:04 PM"},
	{"es-AR", cldr.FormatEsAR, "1.234.567,89", "$ 1.234,56", "50%", "2/1/2006", "3:04 PM", "2/1/2006 3:04 PM"},
	{"es-CO", cldr.FormatEsCO, "1.234.567,89", "$ 1.234,56", "50%", "2/01/2006", "3:04 PM", "2/01/2006 3:04 PM"},
	{"es-CL", cldr.FormatEsCL, "1.234.567,89", "$1.234,56", "50%", "02-01-2006", "3:04 PM", "02-01-2006 3:04 PM"},
	{"it-IT", cldr.FormatItIT, "1.234.567,89", "1.234,56 $", "50%", "02/01/2006", "15:04", "02/01/2006 15:04"},
	{"pt-BR", cldr.FormatPtBR, "1.234.567,89", "$ 1.234,56", "50%", "02/01/2006", "15:04", "02/01/2006 15:04"},
	{"pt-PT", cldr.FormatPtPT, "1 234 567,89", "1 234,56 $", "50%", "02/01/2006", "15:04", "02/01/2006 15:04"},
	{"nl-NL", cldr.FormatNlNL, "1.234.567,89", "$ 1.234,56", "50%", "02-01-2006", "15:04", "02-01-2006 15:04"},
	{"pl-PL", cldr.FormatPlPL, "1 234 567,89", "1 234,56 $", "50%", "2.01.2006", "15:04", "2.01.2006 15:04"},
	{"cs-CZ", cldr.FormatCsCZ, "1 234 567,89", "1 234,56 $", "50 %", "02.01.2006", "15:04", "02.01.2006 15:04"},
	{"uk-UA", cldr.FormatUkUA, "1 234 567,89", "1 234,56 $", "50%", "02.01.2006", "15:04", "02.01.2006 15:04"},
	{"ru-RU", cldr.FormatRuRU, "1 234 567,89", "1 234,56 $", "50 %", "02.01.2006", "15:04", "02.01.2006 15:04"},
	{"tr-TR", cldr.FormatTrTR, "1.234.567,89", "$1.234,56", "50%", "2.01.2006", "15:04", "2.01.2006 15:04"},
	{"ar-AE", cldr.FormatArAE, "1,234,567.89", "1,234.56 $", "50%", "2/1/2006", "3:04 PM", "2/1/2006 3:04 PM"},
	{"ja-JP", cldr.FormatJaJP, "1,234,567.89", "$1,234.56", "50%", "2006/01/02", "15:04", "2006/01/02 15:04"},
	{"zh-CN", cldr.FormatZhCN, "1,234,567.89", "$1,234.56", "50%", "2006/1/2", "15:04", "2006/1/2 15:04"},
	{"ko-KR", cldr.FormatKoKR, "1,234,567.89", "$1,234.56", "50%", "2006. 1. 2.", "PM 3:04", "2006. 1. 2. PM 3:04"},
}

// refTime is Go's own reference instant: the layouts are all-numeric (no
// locale-specific month or weekday names), so formatting this exact instant
// reproduces each locale's layout string verbatim when, and only when, the
// layout is a well-formed Go layout in the locale's actual field order.
var refTime = time.Date(2006, time.January, 2, 15, 4, 5, 0, time.UTC)

// TestFormatsRenderAllLocales wires every shipped spec into one bundle and
// renders a number, a currency amount, a percentage, and a date/time through
// package i18n's engines, checking the exact locale-correct string. A wrong
// separator, a transposed date field, or a flipped currency-before flag fails
// here, not just a field-equality check against the spec's own data.
func TestFormatsRenderAllLocales(t *testing.T) {
	opts := make([]i18n.Option, 0, 2*len(renderCases))
	for _, rc := range renderCases {
		opts = append(opts,
			i18n.WithTranslations(rc.tag, "app", map[string]any{"x": "y"}),
			i18n.WithFormat(rc.tag, rc.spec),
		)
	}
	b, err := i18n.New("en-US", opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	dollar := i18n.Money{Symbol: "$", Minor: 123456, Digits: 2}

	for _, rc := range renderCases {
		t.Run(rc.tag, func(t *testing.T) {
			loc, err := b.Parse(rc.tag)
			if err != nil {
				t.Fatalf("%s not registered: %v", rc.tag, err)
			}
			for _, c := range []struct{ name, got, want string }{
				{"Number", b.Number(loc, 1234567.89), rc.number},
				{"Currency", b.Currency(loc, dollar), rc.currency},
				{"Percent", b.Percent(loc, 0.5), rc.percent},
				{"Date", b.Date(loc, refTime), rc.date},
				{"Time", b.Time(loc, refTime), rc.clock},
				{"DateTime", b.DateTime(loc, refTime), rc.dateTime},
			} {
				if c.got != c.want {
					t.Errorf("%s: %s = %q, want %q", rc.tag, c.name, c.got, c.want)
				}
			}
		})
	}
}

// TestTurkishPercentGap documents a known FormatSpec limitation surfaced by
// tr-TR: CLDR's Turkish percent pattern is "%50" (sign leads the number).
// FormatSpec/PercentSpace can only place a space before a trailing sign, so
// the shared engine renders "50%" here: not canonically Turkish, but the most
// FormatSpec can express. This pins the actual (non-canonical) behavior so a
// future engine change is a deliberate decision, not a silent diff.
func TestTurkishPercentGap(t *testing.T) {
	b, err := i18n.New("tr-TR",
		i18n.WithTranslations("tr-TR", "app", map[string]any{"x": "y"}),
		i18n.WithFormat("tr-TR", cldr.FormatTrTR),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := b.Percent(b.Default(), 0.5), "50%"; got != want {
		t.Errorf("Percent = %q, want %q", got, want)
	}
}
