// Package cldr carries CLDR plural rules and locale format specs for
// package i18n.
//
// It is data, not policy. package i18n imports nothing from this package and
// applies none of it automatically — the dependency arrow runs one way, and
// the compiler enforces it. Wire what you want, explicitly.
//
// # Usage
//
//	bundle, err := i18n.New("uk-UA",
//	    i18n.WithTranslations("uk-UA", "app", map[string]any{"greeting": "Привіт"}),
//	    i18n.WithPlural("uk", cldr.Uk),
//	    i18n.WithFormat("uk-UA", cldr.FormatUkUA),
//	)
//
// A locale you do not wire is not broken: package i18n falls back to its
// zero-one-many DefaultRule and to Invariant formatting.
//
// # Keys
//
// Plural rules are keyed by base language (Uk, Pl, Ar), since plural grammar
// rarely varies by region. Among the languages here, CLDR splits one:
// European Portuguese. PtPT reads 0 as other, and Pt reads it as one. Wire
// both, and package i18n picks the rule by tag, then base language:
//
//	i18n.WithPlural("pt", cldr.Pt), i18n.WithPlural("pt-PT", cldr.PtPT)
//
// Format specs are keyed by locale tag (FormatEsMX, FormatEsES) — es-MX and
// es-ES pluralize identically and format nothing alike. A format spec's tag
// must match your catalog's locale tag: FormatUkUA takes effect for a uk-UA
// locale and not for a bare uk locale, which falls through to Invariant.
//
// # No family buckets
//
// Every language has its own rule, even where two share an implementation.
// Bucketing languages by family is how "Slavic" rules made Ukrainian 21 read
// as many (CLDR says one) and "Romance" rules made Portuguese 0 read as
// other (CLDR says one). Uk and Ru share a body; they do not share an
// identifier.
//
// # Verification
//
// Every rule is checked against testdata/plurals.json, a table generated from
// the cardinal rules of CLDR 48 (unicode-org/cldr-json) for 1,211 whole
// numbers. The benchmarks module also checks every rule against
// golang.org/x/text, with a documented exception list where x/text's older
// CLDR is stale: the Romance whole-millions "many" of CLDR 38, Maltese "two"
// of CLDR 42, and Hebrew and Marathi. x/text stays out of this module.
package cldr
