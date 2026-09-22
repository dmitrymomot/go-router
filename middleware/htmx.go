package middleware

import (
	"io"
	"net/http"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/htmx"
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
// It only touches a request that wants a fragment, as [htmx.WantsPartial]
// decides, and like it, it adds HX-Request and HX-Request-Type to Vary. A
// browser request keeps its redirect, and so does an htmx 4 full request, such
// as a boosted link or a history restore: fetch follows the redirect, and htmx
// puts the final URL in the history.
//
// It answers an error of a partial request itself, through
// [router.HandleError], so a redirect that the error handler writes is turned
// as well. That commits the answer, so a middleware inside it that replaces an
// error after next still works, and one outside it no longer does.
//
// Put it outside [Idempotency], so a replayed redirect is turned for the
// request that asks again; see Order in the package doc.
func HTMXRedirect[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return HTMXRedirectWithConfig[C](HTMXRedirectConfig{})(next)
}

// HTMXRedirectWithConfig is [HTMXRedirect] with a configuration.
func HTMXRedirectWithConfig[C router.Context](cfg HTMXRedirectConfig) router.Middleware[C] {
	header := htmx.HeaderRedirect
	if cfg.Location {
		header = htmx.HeaderLocation
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			if !htmx.WantsPartial(c) {
				return next(c)
			}

			res := c.Response()

			w := &hxRedirectWriter{ResponseWriter: res.ResponseWriter, header: header}
			res.ResponseWriter = w
			defer func() {
				res.ResponseWriter = w.ResponseWriter
				if w.converted {
					res.Status = http.StatusOK
				}
			}()

			// The error handler answers here, while the writer is in place, so a
			// redirect it writes, such as one to a sign-in page, is turned too.
			// The router and Logger then skip their own call.
			err := next(c)
			router.HandleError(c, err)
			return err
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
