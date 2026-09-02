package static

import (
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// PathParam is the route parameter that carries the asset path. [Mount]
// registers it, and [Handler] reads it.
const PathParam = "assetpath"

// Handler serves a from a route of your own, which suits a set behind
// middleware. Register it on a pattern ending in "{assetpath...}", or see
// [Mount] for the ordinary case.
//
// It reports [router.ErrMethodNotAllowed] for a method other than GET and
// HEAD, and [router.ErrNotFound] for a path the set does not hold, unless
// Config.NotFound named a handler.
//
// Handler panics if a is nil.
func Handler[C router.Context](a *Assets) router.HandlerFunc[C] {
	if a == nil {
		panic("static: Handler needs an asset set")
	}
	return func(c C) error {
		res := c.Response()
		err := a.serve(res, c.Request(), assetPath(a, c))
		switch {
		case err == nil:
			return nil
		case errors.Is(err, errMethod):
			res.Header().Set(router.HeaderAllow, allowedMethods)
			return router.ErrMethodNotAllowed
		case errors.Is(err, errNoFile):
			// Config.NotFound was read only by Assets.ServeHTTP, so mounting the
			// asset set on a router silently answered with the router's own 404
			// and the configured handler never ran.
			if a.hasNotFound {
				a.notFound.ServeHTTP(res, c.Request())
				return nil
			}
			return router.ErrNotFound
		default:
			return router.ErrInternalServerError.WithError(err)
		}
	}
}

func assetPath(a *Assets, c router.Context) string {
	if tail := c.Param(PathParam); tail != "" {
		return "/" + tail
	}
	p := c.Request().URL.Path
	if a.prefix == "" {
		return p
	}
	if rest, ok := strings.CutPrefix(p, a.prefix); ok && (rest == "" || rest[0] == '/') {
		return rest
	}
	return p
}

// Mount registers a on r under its own prefix, for GET and HEAD.
//
// Mount panics if r or a is nil.
func Mount[C router.Context](r *router.Router[C], a *Assets) {
	if r == nil {
		panic("static: Mount needs a router")
	}
	if a == nil {
		panic("static: Mount needs an asset set")
	}
	h := Handler[C](a)
	prefix := a.Prefix()
	sub := path.Join(prefix, "{"+PathParam+"...}")
	for _, p := range [...]string{prefix, sub} {
		r.Handle(http.MethodGet, p, h)
		r.Handle(http.MethodHead, p, h)
	}
}
