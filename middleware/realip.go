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
// [NewTrustSet]. Headers names the headers to read, in order of preference;
// an empty list reads none, so a server behind a proxy has to name them.
//
// Leftmost takes the first address of the chain in place of the nearest
// untrusted hop. The first address is whatever the client wrote, so use it
// only where the chain itself is trusted.
type RealIPConfig struct {
	Skip     func(c router.Context) bool
	Trust    *TrustSet
	Headers  []string
	Leftmost bool
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

// RealIP reads no header and deletes every forwarding header of an untrusted
// peer, which is the safe default: a server with no proxy in front cannot be
// told a false client address.
//
// Name the headers your proxy actually sets, through [RealIPWithConfig], to
// have the address of the client replace RemoteAddr.
func RealIP[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return RealIPWithConfig[C](RealIPConfig{})(next)
}

// RealIPWithConfig is [RealIP] with a configuration. It rewrites RemoteAddr
// with the address the named headers give, when the peer is trusted, and
// X-Forwarded-Proto then also decides [router.Base.Scheme].
//
// A header the configuration does not name is deleted, so a later handler
// cannot read one this middleware did not check. Every forwarding header of an
// untrusted peer is deleted.
func RealIPWithConfig[C router.Context](cfg RealIPConfig) router.Middleware[C] {
	if cfg.Trust == nil {
		cfg.Trust = NewTrustSet()
	}
	cfg.Headers = canonicalHeaders(cfg.Headers...)
	unnamed := slices.DeleteFunc(slices.Clone(forwardingHeaders), func(name string) bool {
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

			del := unnamed
			if !vouched {
				del = forwardingHeaders
			}
			carries := slices.ContainsFunc(del, func(name string) bool {
				return len(req.Header.Values(name)) > 0
			})
			if h.addr == "" && h.proto == "" && !carries {
				return next(c)
			}

			r := new(http.Request)
			*r = *req
			if h.addr != "" {
				r.RemoteAddr = h.addr
			}
			if h.proto != "" || carries {
				r.Header = req.Header.Clone()
				if carries {
					for _, name := range del {
						r.Header.Del(name)
					}
				}
				if h.proto != "" {
					r.Header.Set(router.HeaderXForwardedProto, h.proto)
				}
			}
			c.SetRequest(r)
			return next(c)
		}
	}
}

// ClientIP reports the address of the peer, without its port. Put [RealIP] in
// front for this to be the address of the client rather than of the proxy.
func ClientIP[C router.Context](c C) string {
	host, _, err := net.SplitHostPort(c.Request().RemoteAddr)
	if err != nil {
		return c.Request().RemoteAddr
	}
	return host
}

type hop struct {
	addr  string
	proto string
}

func (cfg RealIPConfig) client(req *http.Request) (hop, bool) {
	if !cfg.Leftmost {
		peer, _, ok := parseHop(req.RemoteAddr)
		if !ok || !cfg.Trust.Trusted(peer) {
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

func leftmostHop(values []string, rfc7239 bool) hop {
	for _, v := range values {
		for e := range strings.SplitSeq(v, ",") {
			if e = strings.TrimSpace(e); e == "" {
				continue
			}
			h := parseEntry(e, rfc7239)
			if _, text, ok := parseHop(h.addr); ok {
				h.addr = text
				return h
			}
		}
	}
	return hop{}
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
	if s = strings.TrimSpace(s); s == "" {
		return netip.Addr{}, "", false
	}
	// ParseAddrPort allocates an error for every hop without a port, which is
	// most of them. A bare IPv6 address has colons too, so the bracket is what
	// tells the two apart.
	if strings.IndexByte(s, ':') >= 0 && (s[0] == '[' || strings.Count(s, ":") == 1) {
		if ap, err := netip.ParseAddrPort(s); err == nil {
			return ap.Addr(), ap.String(), true
		}
	}
	if addr, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		return addr, addr.String(), true
	}
	return netip.Addr{}, "", false
}
