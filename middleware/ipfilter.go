package middleware

import (
	"fmt"
	"net/netip"
	"slices"

	"github.com/dmitrymomot/go-router"
)

// IPFilterConfig configures [IPFilterWithConfig].
//
// Deny always wins: a client in a Deny range is refused, even one that Allow
// names too. A non-empty Allow is a gate: a client outside every Allow range
// is refused, and so is a request whose address does not parse, as under a
// Unix socket. With Allow empty, every client that Deny does not name passes.
// At least one of the lists is required. An IPv4-mapped prefix such as
// ::ffff:10.0.0.0/104 counts as the IPv4 prefix it maps.
//
// The client is the address that [ClientAddr] reports, so put [RealIP] in
// front where a proxy is, with a Trust that names it: behind an untrusted
// header, a client chooses the address it is filtered by.
//
// A refused request gets [router.ErrForbidden]. OnReject answers it: err is
// the 403 the middleware would report, and its cause names the address.
// Return an error to report it in place of the request, or nil to say that
// OnReject wrote the answer itself; either way the request goes no further. A
// nil OnReject reports err.
type IPFilterConfig[C router.Context] struct {
	Skip     func(c C) bool
	OnReject func(c C, err error) error
	Allow    []netip.Prefix
	Deny     []netip.Prefix
}

// IPFilter lets through only the clients inside allow, such as the ranges of
// an office in front of an admin scope, and refuses the rest with a 403.
//
// See Order in the package doc for where it goes.
//
// IPFilter panics on no prefix, and on a prefix that is not valid.
func IPFilter[C router.Context](allow ...netip.Prefix) router.Middleware[C] {
	return IPFilterWithConfig(IPFilterConfig[C]{Allow: allow})
}

// IPFilterWithConfig is [IPFilter] with a configuration, which also takes a
// list of ranges to refuse.
//
// IPFilterWithConfig panics when both lists are empty, and on a prefix that is
// not valid, such as the zero Prefix that netip.ParsePrefix returns beside its
// error.
func IPFilterWithConfig[C router.Context](cfg IPFilterConfig[C]) router.Middleware[C] {
	if len(cfg.Allow) == 0 && len(cfg.Deny) == 0 {
		panic("middleware: IPFilterConfig needs an Allow or a Deny prefix")
	}
	allow := filterPrefixes("Allow", cfg.Allow)
	deny := filterPrefixes("Deny", cfg.Deny)

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}
			addr, ok := ClientAddr(c)
			if ipAllowed(allow, deny, addr, ok) {
				return next(c)
			}
			var cause error
			if ok {
				cause = fmt.Errorf("middleware: the client %s is not allowed", addr)
			} else {
				cause = fmt.Errorf("middleware: the peer %q has no IP address to allow", c.Request().RemoteAddr)
			}
			err := router.ErrForbidden.WithError(cause)
			if cfg.OnReject == nil {
				return err
			}
			return cfg.OnReject(c, err)
		}
	}
}

func ipAllowed(allow, deny []netip.Prefix, addr netip.Addr, ok bool) bool {
	contains := func(p netip.Prefix) bool { return p.Contains(addr) }
	if ok && slices.ContainsFunc(deny, contains) {
		return false
	}
	if len(allow) > 0 {
		return ok && slices.ContainsFunc(allow, contains)
	}
	return true
}

// filterPrefixes checks each prefix, masks it, and turns an IPv4-mapped one
// into its IPv4 form, which is what ClientAddr reports.
func filterPrefixes(field string, in []netip.Prefix) []netip.Prefix {
	out := make([]netip.Prefix, len(in))
	for i, p := range in {
		if !p.IsValid() {
			panic(fmt.Sprintf("middleware: IPFilterConfig.%s[%d] is not a valid prefix", field, i))
		}
		if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
		}
		out[i] = p.Masked()
	}
	return out
}
