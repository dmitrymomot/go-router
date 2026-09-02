package router

import (
	"testing"
)

func TestNilHandlerAdaptersPanicAtConstruction(t *testing.T) {
	tests := map[string]func(){
		"WrapHandler":     func() { WrapHandler[*tctx](nil) },
		"WrapHandlerFunc": func() { WrapHandlerFunc[*tctx](nil) },
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
