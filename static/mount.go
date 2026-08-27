package static

import (
	"errors"
	"net/http"
	"path"

	"github.com/dmitrymomot/go-router"
)

// PathParam is the catch-all parameter that [Handler] reads the asset path
// from. [Mount] registers it.
const PathParam = "assetpath"

// Handler adapts an asset set to a router handler. It reads the path from the
// [PathParam] catch-all, so the route has to declare it:
//
//	r.GET("/static/{"+static.PathParam+"...}", static.Handler[Ctx](a))
//
// [Mount] registers those routes for you.
//
// A request for a file that the set does not hold returns [router.ErrNotFound],
// so the NotFound handler or the error handler of the router renders the
// answer and Config.NotFound stays unused.
func Handler[C router.Context](a *Assets) router.HandlerFunc[C] {
	return func(c C) error {
		res := c.Response()
		err := a.serve(res, c.Request(), "/"+c.Param(PathParam))
		switch {
		case err == nil:
			return nil
		case errors.Is(err, errMethod):
			res.Header().Set("Allow", allowedMethods)
			return router.ErrMethodNotAllowed
		case errors.Is(err, errNoFile):
			return router.ErrNotFound
		default:
			return router.ErrInternalServerError.WithError(err)
		}
	}
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
