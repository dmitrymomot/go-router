package middleware

import (
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// AllowContentType refuses a request that has a body when the media type of
// its Content-Type is not one of types. The answer is 415
// ([router.ErrUnsupportedMediaType]). A request without a body passes, and a
// body without a Content-Type, or with one that does not parse, is refused.
//
// Write each type as a bare media type, such as "application/json". A subtype
// of "*", as in "image/*", takes every type of that family. The match ignores
// letter case and the parameters of the request, such as the charset.
//
// See Order in the package doc for where it goes.
//
// AllowContentType panics on an empty list, and on an entry that is not a
// media type or carries parameters.
func AllowContentType[C router.Context](types ...string) router.Middleware[C] {
	if len(types) == 0 {
		panic("middleware: AllowContentType needs at least one media type, " +
			`as in AllowContentType("application/json")`)
	}
	exact := make(map[string]struct{}, len(types))
	var families []string
	for _, t := range types {
		mt, params, err := mime.ParseMediaType(t)
		if err != nil || len(params) > 0 || !strings.Contains(mt, "/") {
			panic("middleware: AllowContentType got " + strconv.Quote(t) +
				`, which is not a bare media type; write it as "application/json"`)
		}
		if family, ok := strings.CutSuffix(mt, "/*"); ok {
			families = append(families, family+"/")
			continue
		}
		exact[mt] = struct{}{}
	}
	allowed := strings.Join(types, ", ")

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			req := c.Request()
			if req.ContentLength == 0 || req.Body == nil || req.Body == http.NoBody {
				return next(c)
			}
			mt, _, err := mime.ParseMediaType(req.Header.Get(router.HeaderContentType))
			if err == nil {
				if _, ok := exact[mt]; ok {
					return next(c)
				}
				for _, f := range families {
					if strings.HasPrefix(mt, f) {
						return next(c)
					}
				}
			}
			return router.ErrUnsupportedMediaType.WithMessage("the Content-Type must be one of: %s", allowed)
		}
	}
}
