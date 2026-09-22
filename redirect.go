package router

import (
	"cmp"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
)

// Redirect registers GET, and so HEAD, on pattern and answers it with status and
// target. Each {name} or {name...} of target takes the value of that parameter of
// the route (path, scope prefix or host), path-escaped before '?' and
// query-escaped after. target is written as the client sees it: the scope prefix
// is not added. The query of the request is kept, as RedirectTrailingSlash keeps
// it; when target has a query of its own, the target's pairs come first, so
// Query().Get reads the target's value, and a handler that reads every value
// sees the request's too. A value the target pattern refuses, or one that would
// make the Location start with "//", answers [ErrNotFound]. A method-preserving
// move of a POST needs a handler that calls [Base.Redirect].
//
// Redirect panics on a status that is not a redirect, a target that is not a
// path, a target that names a parameter the route lacks or that only some hosts
// of the scope declare, a target equal to the route, and wherever Handle does.
func (r *Router[C]) Redirect(pattern, target string, status int) {
	if !isRedirectStatus(status) {
		panic(fmt.Sprintf("router: Redirect needs a redirect status, not %d", status))
	}
	if !strings.HasPrefix(target, "/") {
		panic(fmt.Sprintf("router: Redirect needs a target path that starts with \"/\", not %q", target))
	}
	tmpl, err := compileTemplate(target, r.class)
	if err != nil {
		panic(err.Error())
	}
	full := joinPattern(r.scopePrefix(), pattern)
	_, names, err := parsePattern(full, r.class)
	if err != nil {
		panic(err.Error())
	}
	names = append(names, r.sharedHostNames()...)
	for _, name := range tmpl.names {
		if !slices.Contains(names, name) {
			panic(fmt.Sprintf("router: the redirect target %q names the parameter %q, which the route %q does not have", target, name, full))
		}
	}
	if !tmpl.hasQuery && normalizePattern(target) == normalizePattern(full) {
		panic(fmt.Sprintf("router: the redirect of %q points at itself", full))
	}

	r.handle(http.MethodGet, pattern, func(c C) error {
		b := c.base()
		loc, err := tmpl.expand(b.ParamOK)
		if err != nil {
			return ErrNotFound.WithError(err)
		}
		if q := b.req.URL.RawQuery; q != "" {
			sep := "?"
			if tmpl.hasQuery {
				sep = "&"
			}
			loc += sep + q
		}
		return b.Redirect(status, loc)
	}, nil)
}

// sharedHostNames lists the host parameters that every pattern of the nearest
// host scope declares, so a route under it has them whichever host matched.
func (r *Router[C]) sharedHostNames() []string {
	for s := r; s != nil; s = s.owner {
		if len(s.hosts) == 0 {
			continue
		}
		shared := slices.Clone(s.hosts[0].names)
		for _, spec := range s.hosts[1:] {
			shared = slices.DeleteFunc(shared, func(n string) bool { return !slices.Contains(spec.names, n) })
		}
		return shared
	}
	return nil
}

// RedirectHost answers every request for a host that matches pattern with a
// redirect to target, keeping the scheme ([Base.Scheme]), the port, the path and
// the query: r.RedirectHost("www."+apex, apex, http.StatusMovedPermanently).
// target is a host that may name the parameters of pattern, as in
// "{tenant}.example.com", and may carry a port of its own, which replaces the
// port of the request. Like any host scope it wins over routes registered outside
// host scopes, so a pattern of "*" also catches a host that nothing else names.
// The host belongs to the redirect: no other route may be registered for
// pattern, before or after, and that holds in every router this one is mounted
// into, which must mount it at "/". A request that already names target gets
// [ErrNotFound] rather than a redirect loop. Use 308 to keep the method and body
// of a POST.
//
// RedirectHost panics where Host does, on a scope with a prefix, on a pattern
// that already holds routes, on an empty or wildcard target, a target that holds
// a path or a bad port, a target that names a parameter pattern lacks, a target
// equal to pattern, and a status that is not a redirect.
func (r *Router[C]) RedirectHost(pattern, target string, status int) {
	if !isRedirectStatus(status) {
		panic(fmt.Sprintf("router: RedirectHost needs a redirect status, not %d", status))
	}
	if r.root.started.Load() {
		panic("router: cannot register a host scope after the router started serving")
	}
	r.mustBeOpen("register a host redirect")
	if r.inHost || len(r.hosts) > 0 {
		panic("router: a host scope cannot sit inside another host scope")
	}
	if normalizePattern(r.scopePrefix()) != "/" {
		panic(errRedirectHostPrefix)
	}
	spec, err := parseHostPattern(pattern, r.class)
	if err != nil {
		panic(err.Error())
	}
	host, port := target, ""
	if i := indexOutsideBraces(target, ':'); i >= 0 {
		host, port = target[:i], target[i+1:]
		if !validPort(port) {
			panic(fmt.Sprintf("router: the redirect target %q carries a bad port", target))
		}
	}
	if host == "" {
		panic("router: RedirectHost needs a target host")
	}
	if indexOutsideBraces(host, '/') >= 0 || indexOutsideBraces(host, '?') >= 0 {
		panic(fmt.Sprintf("router: the redirect target %q must be a host alone; RedirectHost keeps the path and query", target))
	}
	tmpl, err := compileTemplate(host, r.class)
	if err != nil {
		panic(err.Error())
	}
	for _, name := range tmpl.names {
		if !slices.Contains(spec.names, name) {
			panic(fmt.Sprintf("router: the redirect target %q names the parameter %q, which %q does not have", target, name, pattern))
		}
	}
	if port == "" && tmpl.host.pattern == spec.pattern {
		panic(fmt.Sprintf("router: the redirect of the host %q points at itself", pattern))
	}
	h := func(c C) error {
		b := c.base()
		to, err := tmpl.expand(b.ParamOK)
		if err != nil {
			return ErrNotFound.WithError(err)
		}
		reqPort := ""
		if _, p, err := net.SplitHostPort(b.req.Host); err == nil {
			reqPort = p
		}
		toPort := cmp.Or(port, reqPort)
		if to == b.Host() && toPort == reqPort {
			return ErrNotFound.WithError(fmt.Errorf("router: the host %q redirects to itself", to))
		}
		if toPort != "" {
			to = net.JoinHostPort(to, toPort)
		}
		uri := b.req.URL.RequestURI()
		if !strings.HasPrefix(uri, "/") {
			uri = "/"
		}
		return b.Redirect(status, b.Scheme()+"://"+to+uri)
	}
	claim := new(hostClaim)
	r.Host(pattern, func(g *Router[C]) {
		g.register(registration[C]{method: anyMethod, pattern: "/", handler: h, claim: claim})
		g.register(registration[C]{method: anyMethod, pattern: "/{" + mountParam + "...}", handler: h, claim: claim})
	})
}

const errRedirectHostPrefix = "router: RedirectHost answers every path of a host, so it cannot sit in a scope with a prefix"
