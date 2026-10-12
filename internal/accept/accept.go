// Package accept reads the Accept header for the router and its packages, so
// every caller ranks a media range by one rule.
package accept

import (
	"strconv"
	"strings"
)

// Match reports how well accept admits offer: the rank of the most specific
// range that names it (2 for type/subtype, 1 for type/*, 0 for */*, -1 for
// none) and the quality that range gives it. A q that does not parse, or lies
// outside 0 to 1, counts as 0, so a malformed range refuses the offer rather
// than letting a wider one through.
func Match(accept, offer string) (rank int, q float64) {
	media, _, _ := strings.Cut(offer, ";")
	typ, sub, ok := strings.Cut(strings.TrimSpace(media), "/")
	if !ok {
		return -1, 0
	}
	rank = -1
	for part := range strings.SplitSeq(accept, ",") {
		rng, params, _ := strings.Cut(part, ";")
		rt, rs, ok := strings.Cut(strings.TrimSpace(rng), "/")
		if !ok {
			continue
		}
		switch r := matchRank(typ, sub, rt, rs); {
		case r > rank:
			rank, q = r, Quality(params)
		case r == rank && r >= 0:
			q = max(q, Quality(params))
		}
	}
	return rank, q
}

func matchRank(typ, sub, rt, rs string) int {
	switch {
	case rt == "*" && rs == "*":
		return 0
	case rs == "*" && strings.EqualFold(rt, typ):
		return 1
	case strings.EqualFold(rt, typ) && strings.EqualFold(rs, sub):
		return 2
	default:
		return -1
	}
}

// Quality reads the q parameter from the parameters that follow a media range
// or content coding, and reports 1 when there is none. It follows the qvalue
// grammar of RFC 9110 section 12.4.2: 0 to 1 with at most three decimals. A q
// that is malformed or out of range counts as 0, so the entry refuses rather
// than admits. Spaces around the name and the value are ignored.
func Quality(params string) float64 {
	for p := range strings.SplitSeq(params, ";") {
		k, v, ok := strings.Cut(p, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		return parseQValue(strings.TrimSpace(v))
	}
	return 1
}

func parseQValue(v string) float64 {
	whole, frac, _ := strings.Cut(v, ".")
	if len(frac) > 3 || (whole != "0" && whole != "1") {
		return 0
	}
	for _, c := range frac {
		if c < '0' || c > '9' || whole == "1" && c != '0' {
			return 0
		}
	}
	q, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return q
}

// EncodingQuality returns the quality an Accept-Encoding header gives coding.
// An entry that names the coding wins over "*", which stands for the codings
// the header does not name, and names match without regard to case. "x-gzip"
// is an alias of "gzip". A coding the header leaves out is 0, except
// "identity", which is acceptable unless the header excludes it with
// identity;q=0 or with "*;q=0" and no identity entry. When an entry repeats,
// the first one counts.
func EncodingQuality(header, coding string) float64 {
	q, star := -1.0, -1.0
	for part := range strings.SplitSeq(header, ",") {
		token, params, _ := strings.Cut(part, ";")
		switch token = strings.TrimSpace(token); {
		case sameCoding(token, coding):
			if q < 0 {
				q = Quality(params)
			}
		case token == "*":
			if star < 0 {
				star = Quality(params)
			}
		}
	}
	switch {
	case q >= 0:
		return q
	case star >= 0:
		return star
	case strings.EqualFold(coding, "identity"):
		return 1
	}
	return 0
}

func sameCoding(token, coding string) bool {
	if strings.EqualFold(token, coding) {
		return true
	}
	return strings.EqualFold(coding, "gzip") && strings.EqualFold(token, "x-gzip")
}

// NegotiateEncoding returns the offer the header ranks highest, or "" when it
// admits none. An earlier offer wins a tie, so the order of offers is the
// server's preference.
func NegotiateEncoding(header string, offers ...string) string {
	best, bestQ := "", 0.0
	for _, o := range offers {
		if q := EncodingQuality(header, o); q > bestQ {
			best, bestQ = o, q
		}
	}
	return best
}
