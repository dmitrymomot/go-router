package router

import (
	"net/http"
	"testing"
)

func TestNilHandlerAdaptersPanicAtConstruction(t *testing.T) {
	tests := map[string]func(){
		"WrapHandler":     func() { WrapHandler[*tctx](nil) },
		"WrapHandlerFunc": func() { WrapHandlerFunc[*tctx](nil) },
		"WrapMiddleware":  func() { WrapMiddleware[*tctx](nil) },
	}
	for name, fn := range tests {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("want panic")
				}
			}()
			fn()
		})
	}
}

func TestNilRouterCallbacksPanicAtRegistration(t *testing.T) {
	tests := map[string]func(*Router[*tctx]){
		"route middleware": func(r *Router[*tctx]) {
			r.GET("/", echoRoute, nil)
		},
		"Match handler": func(r *Router[*tctx]) { r.Match(nil, "/", nil) },
		"Use":           func(r *Router[*tctx]) { r.Use(nil) },
		"Pre":           func(r *Router[*tctx]) { r.Pre(nil) },
		"With":          func(r *Router[*tctx]) { r.With(nil) },
		"NotFound": func(r *Router[*tctx]) {
			r.NotFound(nil)
		},
		"MethodNotAllowed": func(r *Router[*tctx]) {
			r.MethodNotAllowed(nil)
		},
		"ErrorHandler": func(r *Router[*tctx]) {
			r.ErrorHandler(nil)
		},
	}
	for name, fn := range tests {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("want panic")
				}
			}()
			fn(newTestRouter())
		})
	}
}

func TestWrappedMiddlewareRejectsNilResult(t *testing.T) {
	r := newTestRouter()
	r.Use(WrapMiddleware[*tctx](func(http.Handler) http.Handler { return nil }))
	r.GET("/", echoRoute)
	if rec := do(r, http.MethodGet, "/"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// The inner Response that WrapMiddleware builds when the wrapper substitutes a
// writer carried its own hook list. Only Status, Size and Committed were copied
// back, so a hook registered inside -- KeyAuth's WWW-Authenticate -- was gone by
// the time the 401 was written.
func TestWrapMiddlewareKeepsBeforeHooks(t *testing.T) {
	substituting := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(passThroughWriter{w}, r)
		})
	}

	for name, wrap := range map[string]func(http.Handler) http.Handler{
		"identity":     func(h http.Handler) http.Handler { return h },
		"substituting": substituting,
	} {
		t.Run(name, func(t *testing.T) {
			r := newTestRouter()
			r.Use(WrapMiddleware[*tctx](wrap), func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
				return func(c *tctx) error {
					c.Response().Before(func() { c.Response().Header().Set("X-Hook", "ran") })
					return ErrUnauthorized
				}
			})
			r.GET("/a", echoRoute)

			rec := do(r, http.MethodGet, "/a")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if got := rec.Header().Get("X-Hook"); got != "ran" {
				t.Errorf("hook header = %q, want %q", got, "ran")
			}
		})
	}
}

type passThroughWriter struct{ http.ResponseWriter }
