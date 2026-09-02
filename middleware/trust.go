package middleware

import (
	"net/netip"
	"strconv"
)

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

// TrustPrefix trusts every address in p. A zero Prefix -- what netip.ParsePrefix
// returns beside its error -- panics: stored, it matches nothing, so the proxy
// is never trusted and RealIP quietly strips every forwarding header.
func TrustPrefix(p netip.Prefix) TrustOption {
	if !p.IsValid() {
		panic("middleware: TrustPrefix needs a valid prefix")
	}
	return func(s *TrustSet) { s.prefixes = append(s.prefixes, p.Masked()) }
}

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
