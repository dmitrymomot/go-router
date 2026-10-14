package benchmarks

import (
	"testing"

	xplural "golang.org/x/text/feature/plural"
	"golang.org/x/text/language"

	"github.com/dmitrymomot/go-router/i18n"
	"github.com/dmitrymomot/go-router/i18n/cldr"
)

// This differential test lives here, not beside the package, so the root
// module stays free of third-party dependencies.

// xForm maps x/text's Form to our PluralCategory.
func xForm(f xplural.Form) (i18n.PluralCategory, bool) {
	switch f {
	case xplural.Zero:
		return i18n.Zero, true
	case xplural.One:
		return i18n.One, true
	case xplural.Two:
		return i18n.Two, true
	case xplural.Few:
		return i18n.Few, true
	case xplural.Many:
		return i18n.Many, true
	case xplural.Other:
		return i18n.Other, true
	}
	return 0, false
}

// staleExceptions lists (lang, n) pairs where x/text's CLDR vintage disagrees
// with modern CLDR and OUR rule is the correct one. Every entry needs a reason.
//
// CLDR 38 (2020) added the whole-millions "many" category to the Romance
// languages; x/text predates it and still reports "other".
var staleExceptions = map[string]map[int]string{
	"fr":    {1000000: "CLDR 38 Romance whole-millions many", 2000000: "ditto", 3000000: "ditto", 10000000: "ditto"},
	"es":    {1000000: "CLDR 38 Romance whole-millions many", 2000000: "ditto", 3000000: "ditto", 10000000: "ditto"},
	"it":    {1000000: "CLDR 38 Romance whole-millions many", 2000000: "ditto", 3000000: "ditto", 10000000: "ditto"},
	"pt":    {1000000: "CLDR 38 Romance whole-millions many", 2000000: "ditto", 3000000: "ditto", 10000000: "ditto"},
	"pt-PT": {1000000: "CLDR 38 Romance whole-millions many", 2000000: "ditto", 3000000: "ditto", 10000000: "ditto"},
	"ca":    {1000000: "CLDR 38 Romance whole-millions many", 2000000: "ditto", 3000000: "ditto", 10000000: "ditto"},
	// x/text omits CLDR's i%100!=11 exception for Macedonian, so it
	// wrongly says "one" at every n%10==1 && n%100==11 probe value.
	"mk": {
		11:   "x/text Macedonian bug: omits CLDR i%100!=11 exception",
		111:  "x/text Macedonian bug: omits CLDR i%100!=11 exception",
		211:  "x/text Macedonian bug: omits CLDR i%100!=11 exception",
		1011: "x/text Macedonian bug: omits CLDR i%100!=11 exception",
	},
	// x/text predates CLDR v42, which removed Hebrew's round-tens
	// "many" category; x/text still reports "many" for those values.
	"he": {
		20:       "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		30:       "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		40:       "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		50:       "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		60:       "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		70:       "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		80:       "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		90:       "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		100:      "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		110:      "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		120:      "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		130:      "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		200:      "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		300:      "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		1000:     "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		10000:    "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		100000:   "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		1000000:  "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		2000000:  "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		3000000:  "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
		10000000: "x/text stale: CLDR removed Hebrew round-tens 'many' at v42",
	},
}

// probe covers every band real CLDR rules discriminate on.
func probe() []int {
	ns := make([]int, 0, 256)
	for n := 0; n <= 130; n++ {
		ns = append(ns, n)
	}
	ns = append(ns,
		200, 201, 202, 203, 211, 300, 999, 1000, 1001, 1002, 1011,
		10000, 100000, 1000000, 1000001, 2000000, 3000000, 10000000,
	)
	return ns
}

// TestXtextRulesMatchCLDR is the acceptance criterion for this whole package: every
// rule is differential-tested against x/text's CLDR-generated tables.
func TestXtextRulesMatchCLDR(t *testing.T) {
	for lang, rule := range cldr.All() {
		t.Run(lang, func(t *testing.T) {
			tag := language.MustParse(lang)
			for _, n := range probe() {
				want, ok := xForm(xplural.Cardinal.MatchPlural(tag, n, 0, 0, 0, 0))
				if !ok {
					t.Fatalf("x/text returned no form for %s(%d)", lang, n)
				}
				got := rule(n)
				if got == want {
					continue
				}
				if reason, excepted := staleExceptions[lang][n]; excepted {
					t.Logf("known x/text staleness at %s(%d): ours=%v x/text=%v (%s)", lang, n, got, want, reason)
					continue
				}
				t.Errorf("%s(%d) = %v, x/text says %v: verify against the CLDR spec; if x/text is stale, add it to staleExceptions with a reason",
					lang, n, got, want)
			}
		})
	}
}
