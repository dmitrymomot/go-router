package middleware

import "net/netip"

type TrustSet struct {
	prefixes  []netip.Prefix
	loopback  bool
	linkLocal bool
	private   bool
}

type TrustOption func(*TrustSet)

func TrustLoopback(v bool) TrustOption { return func(s *TrustSet) { s.loopback = v } }

func TrustLinkLocal(v bool) TrustOption { return func(s *TrustSet) { s.linkLocal = v } }

func TrustPrivateNet(v bool) TrustOption { return func(s *TrustSet) { s.private = v } }

func TrustPrefix(p netip.Prefix) TrustOption {
	return func(s *TrustSet) { s.prefixes = append(s.prefixes, p.Masked()) }
}

func NewTrustSet(opts ...TrustOption) *TrustSet {
	s := &TrustSet{loopback: true, linkLocal: true, private: true}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

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
