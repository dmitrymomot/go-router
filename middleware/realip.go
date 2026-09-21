package middleware

import (
	"cmp"
	"iter"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// RealIPConfig configures [RealIPWithConfig].
//
// Trust names the peers whose forwarding headers count, and a nil one takes
// [NewTrustSet]. Headers names the headers to read the client address from, in
// order of preference; an empty list reads none, so a server behind a proxy has
// to name them.
//
// X-Forwarded-Proto carries no address and does not go in Headers. A trusted
// peer's scheme is kept, reduced to "http" or "https": the entry that peer
// wrote last, or the first entry under Leftmost. A proto= in a named Forwarded
// header wins over it. An untrusted peer's scheme is always deleted.
//
// A trusted proxy that passes the client's X-Forwarded-Proto through unchanged
// lets the client choose the scheme. Set DropProto behind such a proxy: it
// deletes X-Forwarded-Proto and ignores Forwarded proto=, so
// [router.Base.Scheme] follows the connection alone.
//
// Leftmost takes the first address of the chain in place of the nearest
// untrusted hop, and vouches for the scheme of any peer. The first address is
// whatever the client wrote, so use it only where the chain itself is trusted.
type RealIPConfig struct {
	Skip      func(c router.Context) bool
	Trust     *TrustSet
	Headers   []string
	Leftmost  bool
	DropProto bool
}

var forwardingHeaders = canonicalHeaders(
	router.HeaderForwarded, router.HeaderXForwardedFor, router.HeaderXRealIP,
	router.HeaderXForwardedProto,
)

func canonicalHeaders(names ...string) []string {
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = http.CanonicalHeaderKey(name)
	}
	return out
}

// RealIP reads no address header, which is the safe default: a server with no
// proxy in front cannot be told a false client address. It deletes every
// forwarding header of an untrusted peer. Of a trusted peer it deletes all of
// them except the scheme that peer states.
//
// Name the headers your proxy actually sets, through [RealIPWithConfig], to
// have the address of the client replace RemoteAddr.
func RealIP[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return RealIPWithConfig[C](RealIPConfig{})(next)
}

// RealIPWithConfig is [RealIP] with a configuration. It rewrites RemoteAddr
// with the address the named headers give, when the peer is trusted.
//
// A header the configuration does not name is deleted, so a later handler
// cannot read one this middleware did not check. X-Forwarded-Proto is the
// exception: a trusted peer's scheme is kept, so [router.Base.Scheme] reports
// https behind a proxy that ends TLS. Every forwarding header of an untrusted
// peer is deleted.
//
// RealIPWithConfig panics when Headers names X-Forwarded-Proto.
func RealIPWithConfig[C router.Context](cfg RealIPConfig) router.Middleware[C] {
	if slices.ContainsFunc(cfg.Headers, func(name string) bool {
		return strings.EqualFold(name, router.HeaderXForwardedProto)
	}) {
		panic("middleware: RealIPConfig.Headers names X-Forwarded-Proto, which carries no address; " +
			"a trusted peer's scheme is kept without it, and DropProto deletes it")
	}
	if cfg.Trust == nil {
		cfg.Trust = NewTrustSet()
	}
	cfg.Headers = canonicalHeaders(cfg.Headers...)
	unnamed := slices.DeleteFunc(slices.Clone(forwardingHeaders), func(name string) bool {
		if name == router.HeaderXForwardedProto {
			return !cfg.DropProto
		}
		return slices.ContainsFunc(cfg.Headers, func(named string) bool {
			return strings.EqualFold(name, named)
		})
	})

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}
			req := c.Request()
			h, vouched := cfg.client(req)

			var proto string
			var fix bool
			if vouched && !cfg.DropProto {
				proto, fix = keptProto(cfg.Leftmost, h.proto,
					req.Header.Values(router.HeaderXForwardedProto))
			}
			del := unnamed
			if !vouched {
				del = forwardingHeaders
			}
			carries := slices.ContainsFunc(del, func(name string) bool {
				return len(req.Header.Values(name)) > 0
			})
			if h.addr == "" && !fix && !carries {
				return next(c)
			}

			r := new(http.Request)
			*r = *req
			if h.addr != "" {
				r.RemoteAddr = h.addr
			}
			if fix || carries {
				r.Header = req.Header.Clone()
				for _, name := range del {
					r.Header.Del(name)
				}
				switch {
				case fix && proto != "":
					r.Header.Set(router.HeaderXForwardedProto, proto)
				case fix:
					r.Header.Del(router.HeaderXForwardedProto)
				}
			}
			c.SetRequest(r)
			return next(c)
		}
	}
}

// ClientIP reports the address of the peer, without its port. Put [RealIP] in
// front for this to be the address of the client rather than of the proxy.
// [ClientAddr] reports the same address as a [netip.Addr].
func ClientIP[C router.Context](c C) string {
	host, _, err := net.SplitHostPort(c.Request().RemoteAddr)
	if err != nil {
		return c.Request().RemoteAddr
	}
	return host
}

// ClientAddr reports the address of the peer, the client when [RealIP] runs in
// front, without its port or zone and with an IPv4-mapped address unmapped. ok
// is false when RemoteAddr holds no IP address, as under a Unix socket.
//
// It allocates nothing when it succeeds.
func ClientAddr[C router.Context](c C) (netip.Addr, bool) {
	ap, _, ok := splitHop(c.Request().RemoteAddr)
	if !ok {
		return netip.Addr{}, false
	}
	return ap.Addr().WithZone("").Unmap(), true
}

type hop struct {
	addr  string
	proto string
}

func (cfg RealIPConfig) client(req *http.Request) (hop, bool) {
	if !cfg.Leftmost {
		peer, _, ok := splitHop(req.RemoteAddr)
		if !ok || !cfg.Trust.Trusted(peer.Addr()) {
			return hop{}, false
		}
	}
	var best hop
	for _, name := range cfg.Headers {
		values := req.Header.Values(name)
		if len(values) == 0 {
			continue
		}
		rfc7239 := strings.EqualFold(name, router.HeaderForwarded)
		var h hop
		if cfg.Leftmost {
			h = leftmostHop(values, rfc7239)
		} else {
			h = cfg.trustedHop(values, rfc7239)
		}
		best.proto = cmp.Or(h.proto, best.proto)
		if h.addr != "" {
			best.addr = h.addr
			break
		}
	}
	return best, true
}

func (cfg RealIPConfig) trustedHop(values []string, rfc7239 bool) hop {
	var best hop
	for e := range entriesRight(values) {
		h := parseEntry(e, rfc7239)
		addr, text, ok := parseHop(h.addr)
		if !ok {
			if best.proto == "" {
				best.proto = h.proto
			}
			return best
		}
		h.addr = text
		best = h
		if !cfg.Trust.Trusted(addr) {
			return best
		}
	}
	return best
}

// keptProto reduces the X-Forwarded-Proto of a trusted peer to one scheme, or
// to none. fix reports whether the header has to be rewritten to hold it.
func keptProto(leftmost bool, forwarded string, values []string) (proto string, fix bool) {
	proto = forwarded
	if proto == "" {
		proto = scheme(firstEntry(values, leftmost))
	}
	unchanged := (len(values) == 0 && proto == "") || (len(values) == 1 && values[0] == proto)
	return proto, !unchanged
}

// firstEntry is the first entry of entriesLeft or entriesRight, found without
// an iterator: a closure picked at run time escapes to the heap.
func firstEntry(values []string, leftmost bool) string {
	if leftmost {
		for _, v := range values {
			for v != "" {
				var e string
				e, v, _ = strings.Cut(v, ",")
				if e = strings.TrimSpace(e); e != "" {
					return e
				}
			}
		}
		return ""
	}
	for i := len(values) - 1; i >= 0; i-- {
		for v := values[i]; v != ""; {
			e := v
			if j := strings.LastIndexByte(v, ','); j >= 0 {
				e, v = v[j+1:], v[:j]
			} else {
				v = ""
			}
			if e = strings.TrimSpace(e); e != "" {
				return e
			}
		}
	}
	return ""
}

func leftmostHop(values []string, rfc7239 bool) hop {
	for e := range entriesLeft(values) {
		h := parseEntry(e, rfc7239)
		if _, text, ok := parseHop(h.addr); ok {
			h.addr = text
			return h
		}
	}
	return hop{}
}

func entriesLeft(values []string) iter.Seq[string] {
	return func(yield func(string) bool) {
		for _, v := range values {
			for e := range strings.SplitSeq(v, ",") {
				if e = strings.TrimSpace(e); e != "" && !yield(e) {
					return
				}
			}
		}
	}
}

func entriesRight(values []string) iter.Seq[string] {
	return func(yield func(string) bool) {
		for _, v := range slices.Backward(values) {
			for v != "" {
				e := v
				if j := strings.LastIndexByte(v, ','); j >= 0 {
					e, v = v[j+1:], v[:j]
				} else {
					v = ""
				}
				if e = strings.TrimSpace(e); e != "" && !yield(e) {
					return
				}
			}
		}
	}
}

func parseEntry(e string, rfc7239 bool) hop {
	if !rfc7239 {
		return hop{addr: e}
	}
	var h hop
	for p := range strings.SplitSeq(e, ";") {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch k = strings.TrimSpace(k); {
		case strings.EqualFold(k, "for"):
			h.addr = v
		case strings.EqualFold(k, "proto"):
			h.proto = scheme(v)
		}
	}
	return h
}

func scheme(v string) string {
	switch {
	case strings.EqualFold(v, "https"):
		return "https"
	case strings.EqualFold(v, "http"):
		return "http"
	}
	return ""
}

func parseHop(s string) (netip.Addr, string, bool) {
	ap, hasPort, ok := splitHop(s)
	switch {
	case !ok:
		return netip.Addr{}, "", false
	case hasPort:
		return ap.Addr(), ap.String(), true
	}
	return ap.Addr(), ap.Addr().String(), true
}

func splitHop(s string) (ap netip.AddrPort, hasPort, ok bool) {
	if s = strings.TrimSpace(s); s == "" {
		return netip.AddrPort{}, false, false
	}
	// ParseAddrPort allocates an error for every hop without a port, which is
	// most of them. A bare IPv6 address has colons too, so the bracket is what
	// tells the two apart.
	if strings.IndexByte(s, ':') >= 0 && (s[0] == '[' || strings.Count(s, ":") == 1) {
		if ap, err := netip.ParseAddrPort(s); err == nil {
			return ap, true, true
		}
	}
	if addr, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		return netip.AddrPortFrom(addr, 0), false, true
	}
	return netip.AddrPort{}, false, false
}
