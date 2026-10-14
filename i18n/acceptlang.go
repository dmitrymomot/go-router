package i18n

import (
	"cmp"
	"slices"
	"strings"

	"github.com/dmitrymomot/go-router/internal/accept"
)

// Bounds on Accept-Language, which is attacker-controlled: without them a
// client can make the server parse and sort megabytes of tags.
const (
	// maxAcceptLangLen caps the header size. Real headers are well under 200
	// bytes; 4 KB is generous.
	maxAcceptLangLen = 4096
	// maxAcceptLangTags caps how many tags are parsed. Beyond it the header is
	// truncated in header order — not rejected, and not sorted by q first: the
	// cap has to run before the sort, or an attacker could force the sort to
	// touch every tag it is meant to bound. Clients list ranges
	// most-preferred-first, so in practice the kept prefix is the client's top
	// preferences; only a client that both sends over 32 ranges and orders them
	// against their own q values would drop a preference here.
	maxAcceptLangTags = 32
)

// langQ is one parsed Accept-Language entry.
type langQ struct {
	tag string
	q   float64
}

// parseAcceptLanguage parses and sorts an Accept-Language header, highest
// quality first. Entries with a q of zero, or one that is not an RFC 9110
// qvalue, are dropped. The sort is stable, so equal-q tags keep header order — the
// caller's first supported match among them is the server-preference
// tie-break (RFC 7231 5.3.1).
//
// It appends to out, so a caller can pass a buffer on its stack.
func parseAcceptLanguage(out []langQ, header string) []langQ {
	if header == "" || len(header) > maxAcceptLangLen {
		return out
	}
	for part := range strings.SplitSeq(header, ",") {
		if len(out) >= maxAcceptLangTags {
			break
		}
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tag, params, _ := strings.Cut(part, ";")
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		// Quality reads the q of RFC 9110: 0 to 1 with at most three
		// decimals, 1 when absent, and 0 when malformed.
		q := accept.Quality(params)
		if q <= 0 {
			continue // malformed, or "not acceptable"
		}
		if tag != "*" {
			if tag = normalizeTag(tag); tag == "" {
				continue
			}
		}
		out = append(out, langQ{tag: tag, q: q})
	}
	// slices.SortStableFunc over sort.SliceStable: sort.Slice* builds a
	// reflection-based swapper per call, which profiles show costing ~1/3
	// of Negotiate's allocations for a header this small.
	slices.SortStableFunc(out, func(a, b langQ) int { return cmp.Compare(b.q, a.q) })
	return out
}

// negotiate picks the best supported locale for header. ok is false when
// nothing matches, which lets a caller (e.g. the middleware's resolver chain)
// fall through to another source before defaulting.
func (b *Bundle) negotiate(header string) (int, bool) {
	var buf [maxAcceptLangTags]langQ
	for _, lq := range parseAcceptLanguage(buf[:0], header) {
		if lq.tag == "*" {
			return b.defaultIdx, true
		}
		// The tag is normalized already. resolveIdx applies the exact-then-base
		// matching of Parse, so negotiation and lookup agree by construction.
		if i, ok := b.resolveIdx(Locale{tag: lq.tag}); ok {
			return i, true
		}
	}
	return 0, false
}

// Negotiate picks the best supported locale for an Accept-Language header,
// falling back to the bundle default when the header is empty, malformed, or
// names nothing the bundle supports. Matching runs against the locales the
// application's catalogs declare — there is no framework list to miss.
func (b *Bundle) Negotiate(header string) Locale {
	i, ok := b.negotiate(header)
	if !ok {
		i = b.defaultIdx
	}
	return Locale{tag: b.locales[i].tag}
}
