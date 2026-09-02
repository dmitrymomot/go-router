package middleware

import (
	"io"
	"net/http"

	"github.com/dmitrymomot/go-router"
)

// HTMXRedirectConfig configures [HTMXRedirectWithConfig]. Location sends
// HX-Location, which swaps the new page in, in place of HX-Redirect, which
// loads it whole.
type HTMXRedirectConfig struct {
	Skip     func(c router.Context) bool
	Location bool
}

// HTMXRedirect turns the redirect of a later handler into an htmx one: a 3xx
// with a Location becomes a 200 with HX-Redirect, so the browser navigates
// rather than swapping the redirect target into the page.
//
// It only touches a request that wants a fragment, and it adds the htmx
// headers to Vary. An ordinary form post keeps its redirect.
func HTMXRedirect[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return HTMXRedirectWithConfig[C](HTMXRedirectConfig{})(next)
}

// HTMXRedirectWithConfig is [HTMXRedirect] with a configuration.
func HTMXRedirectWithConfig[C router.Context](cfg HTMXRedirectConfig) router.Middleware[C] {
	header := router.HeaderHXRedirect
	if cfg.Location {
		header = router.HeaderHXLocation
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			res := c.Response()

			router.AddVary(res.Header(),
				router.HeaderHXRequest,
				router.HeaderHXBoosted,
				router.HeaderHXHistoryRestoreRequest,
			)

			if !router.HTMXWantsPartial(c.Request()) {
				return next(c)
			}

			w := &hxRedirectWriter{ResponseWriter: res.ResponseWriter, header: header}
			res.ResponseWriter = w
			defer func() {
				res.ResponseWriter = w.ResponseWriter
				if w.converted {
					res.Status = http.StatusOK
				}
			}()

			return next(c)
		}
	}
}

type hxRedirectWriter struct {
	http.ResponseWriter
	header    string
	converted bool
}

func (w *hxRedirectWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *hxRedirectWriter) WriteString(s string) (int, error) {
	return io.WriteString(w.ResponseWriter, s)
}

func (w *hxRedirectWriter) WriteHeader(code int) {
	if code >= http.StatusMultipleChoices && code < http.StatusBadRequest {
		if loc := w.Header().Get(router.HeaderLocation); loc != "" {
			w.Header().Del(router.HeaderLocation)
			w.Header().Set(w.header, loc)
			w.converted, code = true, http.StatusOK
		}
	}
	w.ResponseWriter.WriteHeader(code)
}
