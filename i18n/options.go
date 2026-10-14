package i18n

import (
	"fmt"
	"io/fs"
)

// config is the internal accumulator New builds from Options. An Option checks
// its own arguments and panics at the call on one that cannot work; reading and
// parsing the catalogs, which can fail on data, happens in New.
type config struct {
	plurals map[string]PluralRule
	formats map[string]FormatSpec
	onMiss  func(Miss)
	sources []fs.FS
	inline  []inlineSource
}

// inlineSource is one WithTranslations call.
type inlineSource struct {
	data map[string]any
	tag  string
	ns   string
}

// Option configures New.
type Option func(*config)

// WithMessages loads catalogs from fsys, laid out as {tag}/{namespace}.json.
// The directory names become the bundle's supported locales — this package
// validates them against no list, so any tag works. May be passed more than
// once; a key defined by two sources is a construction error.
//
// WithMessages panics if fsys is nil.
func WithMessages(fsys fs.FS) Option {
	if fsys == nil {
		panic("i18n: WithMessages needs a file system")
	}
	return func(cf *config) { cf.sources = append(cf.sources, fsys) }
}

// WithTranslations adds one locale's namespace programmatically, equivalent to
// a {tag}/{ns}.json file. data may nest; it is flattened to dot notation.
//
// WithTranslations panics if tag is not a language tag or ns is empty.
func WithTranslations(tag, ns string, data map[string]any) Option {
	if normalizeTag(tag) == "" {
		panic(fmt.Sprintf("i18n: WithTranslations: %q is not a language tag", tag))
	}
	if ns == "" {
		panic("i18n: WithTranslations needs a namespace")
	}
	return func(cf *config) {
		cf.inline = append(cf.inline, inlineSource{tag: tag, ns: ns, data: data})
	}
}

// WithPlural wires a plural rule for a language ("uk") or for one region of it
// ("pt-PT"). Resolution is tag, then base language, then DefaultRule, so a
// rule for "pt" covers pt-BR while a rule for "pt-PT" overrides it there:
// CLDR gives European Portuguese a rule of its own.
//
// This package ships no per-language rules. Correct CLDR rules live in
// package cldr: i18n.WithPlural("uk", cldr.Uk).
//
// WithPlural panics if tag is not a language tag or rule is nil.
func WithPlural(tag string, rule PluralRule) Option {
	if rule == nil {
		panic(fmt.Sprintf("i18n: WithPlural: nil rule for %q", tag))
	}
	t := normalizeTag(tag)
	if t == "" {
		panic(fmt.Sprintf("i18n: WithPlural: %q is not a language tag", tag))
	}
	return func(cf *config) { cf.plurals[t] = rule }
}

// WithFormat wires a FormatSpec for one locale tag. Resolution is tag, then
// base language, then Invariant.
//
// This package ships no per-locale specs. Correct CLDR specs live in package
// cldr: i18n.WithFormat("es-MX", cldr.FormatEsMX).
//
// WithFormat panics if tag is not a language tag.
func WithFormat(tag string, spec FormatSpec) Option {
	t := normalizeTag(tag)
	if t == "" {
		panic(fmt.Sprintf("i18n: WithFormat: %q is not a language tag", tag))
	}
	return func(cf *config) { cf.formats[t] = spec }
}

// WithMissingHandler sets the hook called for a missing key at render time and
// for incomplete or dead plural forms at construction. Nil by default.
//
// The handler runs on the render path: it must be fast and non-blocking.
// Offload heavy work (logging to a remote sink, DB writes) to a channel. It
// may be called concurrently, since T and TN are safe for concurrent use.
func WithMissingHandler(h func(Miss)) Option {
	return func(cf *config) { cf.onMiss = h }
}
