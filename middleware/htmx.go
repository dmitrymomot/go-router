package middleware

import (
	"net/http"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// HTMXRedirectConfig configures [HTMXRedirectWithConfig].
type HTMXRedirectConfig struct {
	// Skip passes a request straight to the next handler when it returns true.
	Skip func(c router.Context) bool

	// Location writes HX-Location instead of HX-Redirect. htmx then fetches
	// the new URL and swaps its answer, which keeps the page and its scripts
	// alive, where HX-Redirect loads the whole document again.
	Location bool
}

// HTMXRedirect is [HTMXRedirectWithConfig] with its default config. It is a
// middleware itself, so it goes into Use without a call:
//
//	pages.Use(middleware.HTMXRedirect[Ctx])
func HTMXRedirect[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return HTMXRedirectWithConfig[C](HTMXRedirectConfig{})(next)
}

// HTMXRedirectWithConfig turns the redirect that a handler wrote into the
// client-side redirect that htmx follows, so that a handler which answers a
// browser with a 303 also answers htmx correctly.
//
// It rewrites a 3xx that carries a Location into a 200 whose HX-Redirect
// header holds that location. Without it htmx follows the 3xx inside the
// request that it made and swaps whatever the new location answers into the
// element that asked, which is almost never what a redirect after a form post
// means.
//
// Three kinds of request pass through untouched, because each one wants the
// redirect that the handler wrote:
//
//   - a request that htmx did not make, which is any browser navigation;
//   - a boosted request, which htmx follows itself and swaps as the new page,
//     and which a client-side redirect would turn into a full page load;
//   - a history restore request, which asks for a page the same way.
//
// Mount it on the scope that serves pages, never on an API scope: a client
// that reads a 3xx as "the resource is elsewhere", such as one following a
// canonical redirect, still has to see it.
//
// [router.Base.HX] is the explicit form, and the two compose: a handler that
// calls c.HX().Redirect writes no Location, so this middleware sees nothing to
// rewrite.
func HTMXRedirectWithConfig[C router.Context](cfg HTMXRedirectConfig) router.Middleware[C] {
	header := router.HeaderHXRedirect
	if cfg.Location {
		header = router.HeaderHXLocation
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) || !hxNavigates(c.Request()) {
				return next(c)
			}

			res := c.Response()
			w := &hxRedirectWriter{ResponseWriter: res.ResponseWriter, header: header}
			res.ResponseWriter = w
			defer func() {
				res.ResponseWriter = w.ResponseWriter
				if w.converted {
					// The Response recorded the status that the handler wrote,
					// not the one that went out, and a logger above reads it.
					res.Status = http.StatusOK
				}
			}()

			return next(c)
		}
	}
}

// hxRedirectWriter rewrites the status line of a redirect on its way out.
type hxRedirectWriter struct {
	http.ResponseWriter

	// header is the htmx header that carries the location.
	header string

	// converted reports that a redirect became a 200.
	converted bool
}

// Unwrap returns the writer underneath, so that [http.NewResponseController]
// still reaches the flush, hijack and deadline methods of the server. A
// server-sent event stream under this middleware needs it.
func (w *hxRedirectWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// WriteHeader writes the status line, and turns a redirect into the htmx one.
func (w *hxRedirectWriter) WriteHeader(code int) {
	if code >= http.StatusMultipleChoices && code < http.StatusBadRequest {
		// A 304 carries no Location, and neither does a 300 that lists its
		// choices in the body, so the header is what says "redirect" here.
		if loc := w.Header().Get(router.HeaderLocation); loc != "" {
			w.Header().Del(router.HeaderLocation)
			w.Header().Set(w.header, loc)
			w.converted, code = true, http.StatusOK
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

// hxNavigates reports whether an htmx request wants the browser to navigate
// rather than a redirect that htmx follows for itself.
//
// It reads the headers rather than the context, because [router.Context]
// carries the request and not the htmx helpers of [router.Base].
func hxNavigates(r *http.Request) bool {
	h := r.Header
	return hxTrue(h.Get(router.HeaderHXRequest)) &&
		!hxTrue(h.Get(router.HeaderHXBoosted)) &&
		!hxTrue(h.Get(router.HeaderHXHistoryRestoreRequest))
}

// hxTrue reports whether a header carries the "true" that htmx sends.
func hxTrue(v string) bool { return strings.EqualFold(v, "true") }
