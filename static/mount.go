package static

import (
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// PathParam is the catch-all parameter that [Handler] reads the asset path
// from. [Mount] registers it.
const PathParam = "assetpath"

// Handler adapts an asset set to a router handler. It reads the path from the
// [PathParam] catch-all:
//
//	r.GET("/static/{"+static.PathParam+"...}", static.Handler[Ctx](a))
//
// [Mount] registers those routes for you. A route that declares no such
// parameter falls back to the request path with Config.Prefix removed, so it
// still resolves one file rather than answering everything with the index.
//
// A request for a file that the set does not hold returns [router.ErrNotFound],
// so the NotFound handler or the error handler of the router renders the
// answer and Config.NotFound stays unused.
func Handler[C router.Context](a *Assets) router.HandlerFunc[C] {
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
			return router.ErrNotFound
		default:
			return router.ErrInternalServerError.WithError(err)
		}
	}
}

// assetPath returns the path that the asset set resolves, with the prefix
// already removed.
//
// The catch-all that [Mount] registers carries it. A route that declares no
// such parameter reads an empty one, which would resolve to the index and
// answer every request with the page; fall back to the request path instead,
// so that the route answers for the one file it names.
func assetPath(a *Assets, c router.Context) string {
	if tail := c.Param(PathParam); tail != "" {
		return "/" + tail
	}
	p := c.Request().URL.Path
	if a.prefix == "" {
		return p
	}
	// Cut the prefix on a segment boundary only, so that "/staticfoo" keeps
	// its whole path under the prefix "/static".
	if rest, ok := strings.CutPrefix(p, a.prefix); ok && (rest == "" || rest[0] == '/') {
		return rest
	}
	return p
}

// Mount registers the asset set on r, for GET and HEAD, under the prefix that
// its config named:
//
//	a := static.Must(static.Config{FS: dist, Root: "dist", Prefix: "/static"})
//	static.Mount(r, a)
//
// A prefix of "/" serves the assets at the root, which is what a single page
// application wants. The catch-all that Mount registers there loses to every
// literal route, so an API under "/api" still answers its own requests. Mount
// still registers the prefix itself, so a router that already answers GET "/"
// panics on the conflict.
//
// Use the MountHandler method of the router instead to serve the assets on
// every method and to answer a miss from Config.NotFound.
func Mount[C router.Context](r *router.Router[C], a *Assets) {
	h := Handler[C](a)
	prefix := a.Prefix()
	sub := path.Join(prefix, "{"+PathParam+"...}")
	for _, p := range [...]string{prefix, sub} {
		r.Handle(http.MethodGet, p, h)
		r.Handle(http.MethodHead, p, h)
	}
}
