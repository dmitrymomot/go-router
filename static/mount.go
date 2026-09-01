package static

import (
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/dmitrymomot/go-router"
)

const PathParam = "assetpath"

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
