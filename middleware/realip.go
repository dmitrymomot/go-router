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
type RealIPConfig struct {
	// Skip passes a request straight to the next handler when it returns true.
	Skip func(c router.Context) bool

	// Trust is the set of addresses that the proxies of this deployment
	// occupy. It defaults to [NewTrustSet] with no options, which trusts the
	// loopback, the private and the link-local ranges.
	Trust *TrustSet

	// Headers are the forwarding headers that the proxies of this deployment
	// write themselves, in order of preference. The first one that names an
	// address decides and the rest go unread, except that a Forwarded header
	// reporting the protocol alone still hands that on.
	//
	// There is no default, because the header a proxy writes is a fact about
	// the deployment and no list guesses it. A config that names none reads
	// none and the address of the connection stands. Name the one that the
	// proxy in front of this server appends to:
	//
	//	Headers: []string{router.HeaderXForwardedFor}
	//
	// Every forwarding header this list does not name is deleted from the
	// request, so a header that a proxy relayed rather than wrote reaches
	// neither a later middleware nor the handler.
	//
	// X-Forwarded-Proto is one of those headers, and it is the one that
	// [router.Base.Scheme] and [SecureWithConfig] read. Name it as well for a
	// deployment whose proxy terminates TLS and writes it:
	//
	//	Headers: []string{router.HeaderXForwardedFor, router.HeaderXForwardedProto}
	//
	// Leave it out and the header goes, whoever made the connection, because a
	// proxy that only appends an address leaves the protocol to the client.
	Headers []string

	// Leftmost takes the first entry of the header and reads no trust set at
	// all, which is what this middleware did before.
	//
	// The first entry is the one the client writes, so a client that reaches
	// the server directly names its own address: X-Forwarded-For: 1.2.3.4 and
	// it is 1.2.3.4 to the rate limiter and the audit log. Turn it on only
	// where a proxy in front of the server replaces the header outright rather
	// than appending to it.
	Leftmost bool
}

// forwardingHeaders are the headers that carry the address or the protocol of
// a client past a proxy. [RealIPWithConfig] reads the ones its config names
// and deletes the rest, because a proxy relays the ones it does not write and
// a relayed header says what the client wrote in it.
//
// X-Forwarded-Proto is one of them. A trusted peer is no proof that a proxy
// wrote it: an ingress that appends to X-Forwarded-For and sets no protocol
// leaves the header to the client, and [router.Base.Scheme] then answers with
// what the client chose. Name it in RealIPConfig.Headers for a deployment
// whose proxy writes it.
var forwardingHeaders = []string{
	router.HeaderForwarded, router.HeaderXForwardedFor, router.HeaderXRealIP,
	router.HeaderXForwardedProto,
}

// RealIP is [RealIPWithConfig] with its default config: the standard trust set
// and no header. It reads no forwarding header, so the address of the
// connection stands, and it deletes every forwarding header the request
// carries so that nothing after it reads one. Name the header of the
// deployment through [RealIPWithConfig] to get an address out of it.
//
// It is a middleware itself, so it goes into Use without a call:
//
//	r.Use(middleware.RealIP[Ctx])
func RealIP[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return RealIPWithConfig[C](RealIPConfig{})(next)
}

// RealIPWithConfig replaces the remote address of the request with the address
// of the client that a forwarding header reports.
//
// It reads the headers of Headers and no others, and deletes every forwarding
// header that Headers does not name. Name only a header that the proxies of
// this deployment write themselves. Naming a header that the proxy does not
// itself write makes that header client-controlled: the proxy relays it
// untouched, this middleware reads it as though the proxy had authored it, and
// the client picks whatever address it likes. nginx, HAProxy, an AWS or a GCP
// load balancer, Cloudflare and Envoy all append to X-Forwarded-For, and none
// of them writes or strips the Forwarded header of RFC 7239.
//
//	r.Use(middleware.RealIPWithConfig[Ctx](middleware.RealIPConfig{
//		Headers: []string{router.HeaderXForwardedFor},
//	}))
//
// Within a named header it walks the chain from the server outwards, the last
// entry first and the address of the connection past its end, and stops at the
// first hop outside Trust. That hop is the nearest address the client could
// not forge: every entry to its right belongs to a proxy of this deployment,
// and everything to its left is whatever the client chose to send. A
// connection that no trusted proxy made keeps its own address, and the
// forwarding headers it carries are deleted unread.
//
// This is a change of behaviour. The middleware used to take the leftmost
// entry, which is the entry that the client writes, so anyone could send
// X-Forwarded-For: 1.2.3.4 and become that address in the rate limiter, the
// audit log and the geo check. Leftmost brings the old reading back for a
// deployment whose proxy replaces the header instead of appending to it.
//
// A named Forwarded header reports the protocol as well, and the proto
// parameter of the hop that stands goes into X-Forwarded-Proto, where
// [router.Base.Scheme] and [SecureWithConfig] read the scheme of the request.
//
// X-Forwarded-Proto itself is a forwarding header like the others: it survives
// only while Headers names it, and it is deleted otherwise, on a trusted
// connection as on any other. A trusted peer says that a proxy made the
// connection and not that the proxy wrote this header, and an ingress that
// appends an address without setting a protocol leaves it to the client. The
// scheme decides whether a cookie is marked Secure, whether the answer carries
// HSTS and what an absolute URL points at, so it is the client's to choose
// only where the deployment says a proxy writes it.
func RealIPWithConfig[C router.Context](cfg RealIPConfig) router.Middleware[C] {
	if cfg.Trust == nil {
		cfg.Trust = NewTrustSet()
	}
	// The headers to delete are the ones the config does not name, and neither
	// list changes per request, so the difference is taken once.
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

			// Nothing vouches for the forwarding headers of a connection that
			// no trusted proxy made, so all of them go, the named ones
			// included.
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

			// A shallow copy carries the change, because the request that came
			// in belongs to the server.
			r := new(http.Request)
			*r = *req
			if h.addr != "" {
				r.RemoteAddr = h.addr
			}
			if h.proto != "" || carries {
				// The header map is shared with the request that came in, so
				// it needs a copy of its own before anything writes to it.
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

// ClientIP returns the address of the client, without the port. It reads what
// [RealIPWithConfig] wrote, and falls back to the address of the connection
// when that middleware is not in the chain.
func ClientIP[C router.Context](c C) string {
	host, _, err := net.SplitHostPort(c.Request().RemoteAddr)
	if err != nil {
		return c.Request().RemoteAddr
	}
	return host
}

// hop is one entry of a forwarding header: the address it names, and the
// protocol that reached it, which only RFC 7239 reports.
type hop struct {
	addr  string
	proto string
}

// client returns the hop that stands as the client of this request, and
// whether the connection vouches for the headers at all. The zero hop means
// the address of the connection, which the request already carries.
func (cfg RealIPConfig) client(req *http.Request) (hop, bool) {
	if !cfg.Leftmost {
		// A forwarding header names the client only when a proxy wrote it.
		// A client that reaches the server itself writes whatever it likes.
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
		// A header that reports the protocol and no address, which RFC 7239
		// allows, hands the protocol on and leaves the address to the next one.
		best.proto = cmp.Or(h.proto, best.proto)
		if h.addr != "" {
			best.addr = h.addr
			break
		}
	}
	return best, true
}

// trustedHop walks the entries from the right and returns the first hop that
// no trusted proxy vouched for.
func (cfg RealIPConfig) trustedHop(values []string, rfc7239 bool) hop {
	var best hop
	for e := range entriesRight(values) {
		h := parseEntry(e, rfc7239)
		addr, text, ok := parseHop(h.addr)
		if !ok {
			// The entry names no address: an obfuscated identifier, or an
			// element that reports the protocol alone. Nothing to its left
			// carries any weight, so the hop already found stands, and it
			// takes the protocol that this entry reports.
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
	// Every entry named a proxy, so the leftmost one is the client and best
	// already holds it.
	return best
}

// leftmostHop returns the first entry that names an address.
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

// entriesRight yields the comma separated entries of the header values,
// rightmost first, which walks the chain from the server outwards. A header
// that arrived on several lines reads as one list.
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

// parseEntry reads one entry of a forwarding header. An RFC 7239 element
// carries named parameters, of which for and proto matter here; every other
// header carries the address alone.
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

// scheme returns the lower case form of a proto parameter, and an empty string
// for a value that names neither protocol.
func scheme(v string) string {
	switch {
	case strings.EqualFold(v, "https"):
		return "https"
	case strings.EqualFold(v, "http"):
		return "http"
	}
	return ""
}

// parseHop returns the address of one hop, which may carry a port and which
// may hold an IPv6 address in brackets, together with the canonical text of
// the entry. That text is what goes into RemoteAddr, so that nothing a client
// wrote lands there unread.
//
// It keeps the shape of the entry: a hop that named a port keeps it, and a
// hop that named an address alone yields the address alone, because a port
// this side invented would be a port that no client ever used. RemoteAddr may
// therefore hold a bare IP, which [net.SplitHostPort] does not read. [ClientIP]
// reads either shape and is the supported way to ask for the address.
func parseHop(s string) (netip.Addr, string, bool) {
	if s = strings.TrimSpace(s); s == "" {
		return netip.Addr{}, "", false
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr(), ap.String(), true
	}
	if addr, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		return addr, addr.String(), true
	}
	return netip.Addr{}, "", false
}
