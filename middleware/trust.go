package middleware

import (
	"net/netip"
	"strconv"
)

// TrustSet says which peer addresses may set a forwarding header. [RealIP]
// reads it. A set built by [NewTrustSet] is read-only afterwards and safe for
// concurrent use; the zero TrustSet trusts nothing.
type TrustSet struct {
	prefixes  []netip.Prefix
	loopback  bool
	linkLocal bool
	private   bool
}

// TrustOption configures a [TrustSet].
type TrustOption func(*TrustSet)

// TrustLoopback trusts 127.0.0.0/8 and ::1, which is where a proxy on the same
// machine comes from. It is on by default.
func TrustLoopback(v bool) TrustOption { return func(s *TrustSet) { s.loopback = v } }

// TrustLinkLocal trusts 169.254.0.0/16 and fe80::/10. It is on by default.
func TrustLinkLocal(v bool) TrustOption { return func(s *TrustSet) { s.linkLocal = v } }

// TrustPrivateNet trusts the private ranges, which is where a load balancer
// inside the network comes from. It is on by default.
func TrustPrivateNet(v bool) TrustOption { return func(s *TrustSet) { s.private = v } }

// TrustPrefix trusts every address in p. A zero Prefix -- what netip.ParsePrefix
// returns beside its error -- panics: stored, it matches nothing, so the proxy
// is never trusted and RealIP quietly strips every forwarding header.
func TrustPrefix(p netip.Prefix) TrustOption {
	if !p.IsValid() {
		panic("middleware: TrustPrefix needs a valid prefix")
	}
	return func(s *TrustSet) { s.prefixes = append(s.prefixes, p.Masked()) }
}

// NewTrustSet builds a set that trusts the loopback, the link-local and the
// private ranges. Turn one off with its option, and add a range of your own
// with [TrustPrefix].
//
// NewTrustSet panics on a nil option.
func NewTrustSet(opts ...TrustOption) *TrustSet {
	s := &TrustSet{loopback: true, linkLocal: true, private: true}
	for i, opt := range opts {
		if opt == nil {
			panic("middleware: NewTrustSet got a nil option at index " + strconv.Itoa(i))
		}
		opt(s)
	}
	return s
}

// Trusted reports whether addr is in the set. A nil set and an invalid address
// both report false.
func (s *TrustSet) Trusted(addr netip.Addr) bool {
	if s == nil || !addr.IsValid() {
		return false
	}
	addr = addr.WithZone("").Unmap()
	switch {
	case s.loopback && addr.IsLoopback():
		return true
	case s.private && addr.IsPrivate():
		return true
	case s.linkLocal && addr.IsLinkLocalUnicast():
		return true
	}
	for _, p := range s.prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
