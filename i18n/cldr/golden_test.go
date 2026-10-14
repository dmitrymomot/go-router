package cldr_test

import (
	"encoding/json/v2"
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/dmitrymomot/go-router/i18n"
	"github.com/dmitrymomot/go-router/i18n/cldr"
)

// testdata/plurals.json holds the category of every probe number for every
// rule of the package, evaluated from the cardinal rules of unicode-org/cldr-json
// (cldr-core/supplemental/plurals.json) for whole numbers: one letter per
// probe, z zero, o one, t two, f few, m many, x other. It is generated, so a
// change to a rule must come with a change to CLDR, not to the file.
type golden struct {
	CLDR   string            `json:"cldr"`
	Probes []int             `json:"probes"`
	Rules  map[string]string `json:"rules"`
}

var letters = map[byte]i18n.PluralCategory{
	'z': i18n.Zero, 'o': i18n.One, 't': i18n.Two, 'f': i18n.Few, 'm': i18n.Many, 'x': i18n.Other,
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
	all := cldr.All()
	if got, want := slices.Sorted(maps.Keys(all)), slices.Sorted(maps.Keys(g.Rules)); !slices.Equal(got, want) {
		t.Fatalf("All() keys = %v, golden (CLDR %s) keys = %v", got, g.CLDR, want)
	}
	for lang, forms := range g.Rules {
		rule := all[lang]
		bad := 0
		for i, n := range g.Probes {
			want := letters[forms[i]]
			if got := rule(n); got != want && bad < 5 {
				bad++
				t.Errorf("%s(%d) = %v, CLDR %s says %v", lang, n, got, g.CLDR, want)
			}
		}
	}
}
