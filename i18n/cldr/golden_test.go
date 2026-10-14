package cldr_test

import (
	"encoding/json/v2"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router/i18n"
	"github.com/dmitrymomot/go-router/i18n/cldr"
)

// testdata/plurals.json holds the category of every probe number for every
// rule of the package, evaluated from the cardinal rules of unicode-org/cldr-json
// (cldr-core/supplemental/plurals.json) for whole numbers. The probes are 0 to
// 1200 and a few larger numbers. Each rule is a list of runs such as "3-10 few":
// every probe from 3 to 10 takes few. The runs cover the probes in order, each
// once. The file is generated, so a change to a rule must come with a change
// to CLDR, not to the file.
type golden struct {
	CLDR   string `json:"cldr"`
	Probes struct {
		From  int   `json:"from"`
		To    int   `json:"to"`
		Extra []int `json:"extra"`
	} `json:"probes"`
	Rules map[string][]string `json:"rules"`
}

var categories = func() map[string]i18n.PluralCategory {
	m := make(map[string]i18n.PluralCategory)
	for _, c := range []i18n.PluralCategory{i18n.Zero, i18n.One, i18n.Two, i18n.Few, i18n.Many, i18n.Other} {
		m[c.String()] = c
	}
	return m
}()

// expand turns runs into the category of each probe, in probe order.
func expand(t *testing.T, lang string, probes []int, runs []string) []i18n.PluralCategory {
	t.Helper()
	out := make([]i18n.PluralCategory, 0, len(probes))
	for _, run := range runs {
		span, name, ok := strings.Cut(run, " ")
		cat, known := categories[name]
		if !ok || !known {
			t.Fatalf("%s: run %q is not \"<from>[-<to>] <category>\"", lang, run)
		}
		loS, hiS, isRange := strings.Cut(span, "-")
		if !isRange {
			hiS = loS
		}
		lo, errLo := strconv.Atoi(loS)
		hi, errHi := strconv.Atoi(hiS)
		if errLo != nil || errHi != nil || lo > hi {
			t.Fatalf("%s: run %q has a bad span", lang, run)
		}
		if len(out) == len(probes) || probes[len(out)] != lo {
			t.Fatalf("%s: run %q does not start at the next probe", lang, run)
		}
		for len(out) < len(probes) && probes[len(out)] <= hi {
			out = append(out, cat)
		}
		if probes[len(out)-1] != hi {
			t.Fatalf("%s: run %q does not end at a probe", lang, run)
		}
	}
	if len(out) != len(probes) {
		t.Fatalf("%s: runs cover %d of %d probes", lang, len(out), len(probes))
	}
	return out
}

func TestRulesMatchCLDRGolden(t *testing.T) {
	data, err := os.ReadFile("testdata/plurals.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	var probes []int
	for n := g.Probes.From; n <= g.Probes.To; n++ {
		probes = append(probes, n)
	}
	probes = append(probes, g.Probes.Extra...)

	all := cldr.All()
	if got, want := slices.Sorted(maps.Keys(all)), slices.Sorted(maps.Keys(g.Rules)); !slices.Equal(got, want) {
		t.Fatalf("All() keys = %v, golden (CLDR %s) keys = %v", got, g.CLDR, want)
	}
	for lang, runs := range g.Rules {
		rule := all[lang]
		bad := 0
		for i, want := range expand(t, lang, probes, runs) {
			if got := rule(probes[i]); got != want && bad < 5 {
				bad++
				t.Errorf("%s(%d) = %v, CLDR %s says %v", lang, probes[i], got, g.CLDR, want)
			}
		}
	}
}

// testdata/formats.json holds, for every spec of the package, the strings that
// the CLDR data of its locale renders for one moment, one amount and a few
// numbers. They come from the CLDR symbols, minimum grouping and number,
// currency, percent and date patterns alone, never from the spec fields or
// the Go layouts, so the test proves each spec says what CLDR says.
type formatGolden struct {
	CLDR    string                       `json:"cldr"`
	Time    time.Time                    `json:"time"`
	Money   i18n.Money                   `json:"money"`
	Locales map[string]map[string]string `json:"locales"`
}

func TestFormatsMatchCLDRGolden(t *testing.T) {
	data, err := os.ReadFile("testdata/formats.json")
	if err != nil {
		t.Fatal(err)
	}
	var g formatGolden
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	all := cldr.AllFormats()
	if got, want := slices.Sorted(maps.Keys(all)), slices.Sorted(maps.Keys(g.Locales)); !slices.Equal(got, want) {
		t.Fatalf("AllFormats() keys = %v, golden (CLDR %s) keys = %v", got, g.CLDR, want)
	}
	for tag, want := range g.Locales {
		b, err := i18n.New(tag,
			i18n.WithTranslations(tag, "app", map[string]any{"k": "v"}),
			i18n.WithFormat(tag, all[tag]),
		)
		if err != nil {
			t.Fatal(err)
		}
		loc := b.Default()
		got := map[string]string{
			"Number":     b.Number(loc, -1234567.891),
			"NumberInt":  b.NumberInt(loc, 1234),
			"NumberInt5": b.NumberInt(loc, 12345),
			"Percent":    b.Percent(loc, 0.5),
			"Currency":   b.Currency(loc, g.Money),
			"Date":       b.Date(loc, g.Time),
			"Time":       b.Time(loc, g.Time),
			"DateTime":   b.DateTime(loc, g.Time),
		}
		for k, w := range want {
			if got[k] != w {
				t.Errorf("%s %s = %q, CLDR %s renders %q", tag, k, got[k], g.CLDR, w)
			}
		}
	}
}
