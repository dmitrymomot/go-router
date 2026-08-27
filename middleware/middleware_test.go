package middleware_test

import (
	"net/http"
	"net/http/httptest"

	"github.com/dmitrymomot/go-router"
)

// appContext is the application context of the tests. It shows the shape that
// an application uses: embed router.Base and add fields.
type appContext struct {
	router.Base
	Tenant string
}

func newRouter() *router.Router[*appContext] {
	return router.New(func(http.ResponseWriter, *http.Request) *appContext {
		return &appContext{Tenant: "acme"}
	})
}

func do(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func get(h http.Handler, target string) *httptest.ResponseRecorder {
	return do(h, httptest.NewRequest(http.MethodGet, target, nil))
}

// skipPath returns a Skip function that passes over one path.
func skipPath(path string) func(router.Context) bool {
	return func(c router.Context) bool { return c.Request().URL.Path == path }
}
