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
	shared    bool
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

// TrustSharedNet trusts 100.64.0.0/10, the shared address space of RFC 6598,
// which carrier NAT, some cloud load balancers and overlay networks such as
// Tailscale hand out. It is off by default.
func TrustSharedNet(v bool) TrustOption { return func(s *TrustSet) { s.shared = v } }

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
// [RealIP] trusts the loopback alone unless its config names a set. Pass
// NewTrustSet() as RealIPConfig.Trust behind a load balancer in a private
// range. Every host in those ranges is then trusted, so a client inside the
// same network can set its own address.
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
	case s.shared && sharedAddressSpace.Contains(addr):
		return true
	}
	for _, p := range s.prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// The ranges that Cloudflare and Fastly publish for the requests their edges
// send, as listed at https://www.cloudflare.com/ips/ and
// https://api.fastly.com/public-ip-list on 2026-10-17. A range they add later
// is not trusted until this list has it, so its requests keep the address of
// the edge; check the lists when you upgrade.
var (
	cloudflareRanges = prefixes(
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
		"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
		"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
		"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
		"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
	)
	fastlyRanges = prefixes(
		"23.235.32.0/20", "43.249.72.0/22", "90.98.0.0/17", "90.98.128.0/18",
		"103.244.50.0/24", "103.245.222.0/23", "103.245.224.0/24", "104.156.80.0/20",
		"140.248.64.0/18", "140.248.128.0/17", "146.75.0.0/17", "151.101.0.0/16",
		"157.52.64.0/18", "167.82.0.0/17", "167.82.128.0/20", "167.82.160.0/20",
		"167.82.224.0/20", "172.111.64.0/18", "185.31.16.0/22", "199.27.72.0/21",
		"199.232.0.0/16",
		"2a04:4e40::/32", "2a04:4e42::/31", "2a04:4e44::/32",
	)
)

func prefixes(list ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(list))
	for i, s := range list {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// TrustCloudflare trusts the edges of Cloudflare, which write the client into
// [HeaderCFConnectingIP]. The ranges are the ones Cloudflare published on
// 2026-10-17.
func TrustCloudflare() TrustOption {
	return func(s *TrustSet) { s.prefixes = append(s.prefixes, cloudflareRanges...) }
}

// TrustFastly trusts the edges of Fastly, which write the client into
// [HeaderFastlyClientIP]. The ranges are the ones Fastly published on
// 2026-10-17.
func TrustFastly() TrustOption {
	return func(s *TrustSet) { s.prefixes = append(s.prefixes, fastlyRanges...) }
}
