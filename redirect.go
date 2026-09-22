package router

import (
	"fmt"
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
