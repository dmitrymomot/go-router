package router

import (
	"net/http"
	"net/url"
	"slices"
)

// Mount grafts a router of the same context type under prefix. The routes of
// sub join the table of this router and take its middleware, so they route as
// cheaply as a route registered here.
//
// sub is closed to further registration afterwards, and it must be a top-level
// router that carries no setting belonging to the router that serves, such as
// MaxBodyBytes, a logger or a cookie codec. An error handler of sub answers
// the errors of its routes, and under a prefix other than "/" the 404 and 405
// answers under that prefix too, wrapped in the middleware of sub. The classes
// that sub declared with [Router.ParamClass] stay with its routes.
//
// Mount panics if sub is nil, is a scope of another router, is mounted inside
// itself, or carries such a setting, if sub and this router register one
// host pattern whose classes they declare apart or that both give an error
// handler, or if a [Router.RedirectHost] of sub lands under a prefix or on a
// host that this router already uses.
func (r *Router[C]) Mount(prefix string, sub *Router[C]) {
	if sub == nil {
		panic("router: Mount needs a router")
	}
	if sub.root != sub {
		panic("router: Mount needs a top-level router; a scope of another router cannot be mounted, " +
			"because closing it would close the router that owns it")
	}
	if slices.Contains(r.lineage(), sub) {
		panic("router: a router is mounted inside itself")
	}
	sub.mustNotCarryRootOnlySettings()

	shim := r.newChild(prefix, nil, nil)
	defer r.guard("mount a router")()
	shim.children = append(shim.children, sub)
	shim.mounted = sub
	// The subtree registers into this parent from now on, so replay what it
	// already holds and close it: a later route would have nowhere to go.
	sub.owner = shim
	if sub.hasRoutes {
		// install does not mark the owners, so Use above the mount would pass
		// its guard and apply to none of these routes.
		for s := shim; s != nil; s = s.owner {
			s.hasRoutes = true
		}
	}
	sub.installSubtree()
	closeSubtree(sub)
	r.settingChanged()
}

// closeSubtree shuts a mounted subtree to further registration: its routes are
// replayed into the parent, so a later one would land in a trie nobody serves.
// Per-scope on purpose, so a router can still be mounted into two parents.
func closeSubtree[C Context](r *Router[C]) {
	r.closed = true
	for _, ch := range r.children {
		closeSubtree(ch)
	}
}

// mustNotCarryRootOnlySettings refuses a mount that would lose a setting.
// These live on the root the request path reads, one per served router.
func (r *Router[C]) mustNotCarryRootOnlySettings() {
	lost := ""
	switch {
	case len(r.preMws) > 0:
		lost = "Pre middleware"
	case r.observer != nil:
		lost = "an observer"
	case !r.eng.autoOptions:
		lost = "HandleOPTIONS(false)"
	case r.eng.redirectSlash:
		lost = "RedirectTrailingSlash(true)"
	case r.ropts.maxBody != DefaultMaxBodyBytes:
		lost = "MaxBodyBytes"
	case r.ropts.maxMultipart != 0:
		lost = "MaxMultipartMemory"
	case r.ropts.logger != nil:
		lost = "a logger"
	case len(r.ropts.jsonOpts) > 0:
		lost = "JSONOptions"
	case r.ropts.codec != nil:
		lost = "a cookie codec"
	default:
		return
	}
	panic("router: the mounted router carries " + lost +
		", which belongs to the router that serves; set it on the parent instead")
}

func (r *Router[C]) installSubtree() {
	for _, reg := range r.regs {
		r.install(reg)
	}
	for _, ch := range r.children {
		ch.installSubtree()
	}
}

// MountRouter grafts a router with a context type of its own under prefix. Use
// it where [Router.Mount] cannot go: sub keeps its own context, middleware and
// error handler, and it sees the path with prefix removed. The [Router.Meta]
// values of this router do not reach its routes.
//
// MountRouter panics if sub is nil.
func (r *Router[C]) MountRouter[D Context](prefix string, sub *Router[D]) {
	if sub == nil {
		panic("router: MountRouter needs a router")
	}
	r.MountHandler(prefix, sub)
}

// MountHandler gives prefix and everything under it to a standard library
// handler. h sees the path with prefix removed, so an [http.FileServer] or a
// third-party mux mounts as it stands.
//
// MountHandler panics if h is nil.
func (r *Router[C]) MountHandler(prefix string, h http.Handler) {
	if h == nil {
		panic("router: MountHandler needs a handler")
	}
	prefix = normalizePattern(prefix)
	handler := func(c C) error {
		b := c.base()
		req := stripMountPrefix(b.req, b.rawTail, b.pathEscaped, b.tailSlash)
		b.SetRequest(req)
		h.ServeHTTP(b.res, req)
		return nil
	}
	r.handle(anyMethod, prefix, handler, nil)
	r.handle(anyMethod, joinPattern(prefix, "/{"+mountParam+"...}"), handler, nil)
}

func stripMountPrefix(r *http.Request, tail string, escaped, tailSlash bool) *http.Request {
	r2 := new(http.Request)
	*r2 = *r
	u := new(url.URL)
	*u = *r.URL
	r2.URL = u

	if tail == "" {
		u.Path, u.RawPath = "/", ""
		return r2
	}
	// The router matched without the trailing slash; the handler below the
	// mount is a stranger to that and needs the path the client sent, or it
	// redirects to a directory URL that routes back through here.
	rest := "/" + tail
	if tailSlash {
		rest += "/"
	}
	u.Path, u.RawPath = rest, ""
	if escaped {
		if unescaped, err := url.PathUnescape(rest); err == nil && unescaped != rest {
			u.Path, u.RawPath = unescaped, rest
		}
	}
	return r2
}
