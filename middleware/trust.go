package middleware

import "net/netip"

// TrustSet is the set of addresses that the proxies of a deployment occupy.
//
// [RealIPWithConfig] walks a forwarding header until it leaves this set, so
// the set says whose forwarding headers the server believes. An address
// outside it is a client, and a client writes whatever it likes.
//
// The zero value trusts nothing. Build one with [NewTrustSet].
type TrustSet struct {
	prefixes  []netip.Prefix
	loopback  bool
	linkLocal bool
	private   bool
}

// TrustOption configures a [TrustSet].
type TrustOption func(*TrustSet)

// TrustLoopback sets whether the loopback addresses, 127.0.0.0/8 and ::1, are
// trusted. They are, because a proxy on the same host reaches the server
// through them.
func TrustLoopback(v bool) TrustOption { return func(s *TrustSet) { s.loopback = v } }

// TrustLinkLocal sets whether the link-local unicast addresses, 169.254.0.0/16
// and fe80::/10, are trusted. They are.
func TrustLinkLocal(v bool) TrustOption { return func(s *TrustSet) { s.linkLocal = v } }

// TrustPrivateNet sets whether the private addresses of RFC 1918 and RFC 4193
// are trusted. They are, because a container network and a private subnet both
// sit inside them.
func TrustPrivateNet(v bool) TrustOption { return func(s *TrustSet) { s.private = v } }

// TrustPrefix adds one prefix to the set. Name the range that the load
// balancer occupies, or a range that a CDN publishes:
//
//	middleware.TrustPrefix(netip.MustParsePrefix("203.0.113.0/24"))
func TrustPrefix(p netip.Prefix) TrustOption {
	return func(s *TrustSet) { s.prefixes = append(s.prefixes, p.Masked()) }
}

// NewTrustSet builds a trust set. It trusts the loopback, the private and the
// link-local addresses unless an option turns one of them off, because that is
// where a proxy sits in a deployment that has one.
//
// Turn all three off to trust nothing but the prefixes that you name:
//
//	middleware.NewTrustSet(
//		middleware.TrustLoopback(false),
//		middleware.TrustPrivateNet(false),
//		middleware.TrustLinkLocal(false),
//		middleware.TrustPrefix(netip.MustParsePrefix("203.0.113.0/24")),
//	)
func NewTrustSet(opts ...TrustOption) *TrustSet {
	s := &TrustSet{loopback: true, linkLocal: true, private: true}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Trusted reports whether addr belongs to a proxy of this deployment.
//
// A nil set trusts nothing, and neither does an invalid address. An
// IPv4-mapped IPv6 address answers as the IPv4 address it holds, so a client
// that reaches a dual-stack listener reads the same either way.
func (s *TrustSet) Trusted(addr netip.Addr) bool {
	if s == nil || !addr.IsValid() {
		return false
	}
	// A prefix carries no zone and holds no mapped address, so the zone comes
	// off first and the mapping after it.
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
