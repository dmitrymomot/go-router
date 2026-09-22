// Package accept reads the Accept header for the router and its packages, so
// every caller ranks a media range by one rule.
package accept

import (
	"math"
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
			rank, q = r, quality(params)
		case r == rank && r >= 0:
			q = max(q, quality(params))
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

func quality(params string) float64 {
	for p := range strings.SplitSeq(params, ";") {
		k, v, ok := strings.Cut(p, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || math.IsNaN(q) || math.IsInf(q, 0) || q < 0 || q > 1 {
			return 0
		}
		return q
	}
	return 1
}
