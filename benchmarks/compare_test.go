// Package benchmarks compares go-router with chi and echo.
package benchmarks

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/labstack/echo/v4"

	"github.com/dmitrymomot/go-router"
)

var routes = []struct{ ours, chi, echo string }{
	{"/", "/", "/"},
	{"/health", "/health", "/health"},
	{"/v1/users", "/v1/users", "/v1/users"},
	{"/v1/users/{id}", "/v1/users/{id}", "/v1/users/:id"},
	{"/v1/users/{id}/sessions/{sid}", "/v1/users/{id}/sessions/{sid}", "/v1/users/:id/sessions/:sid"},
	{"/v1/orgs/{org}/repos/{repo}", "/v1/orgs/{org}/repos/{repo}", "/v1/orgs/:org/repos/:repo"},
	{
		"/v1/orgs/{org}/repos/{repo}/issues/{num}/comments",
		"/v1/orgs/{org}/repos/{repo}/issues/{num}/comments",
		"/v1/orgs/:org/repos/:repo/issues/:num/comments",
	},
	{"/v1/search/repos", "/v1/search/repos", "/v1/search/repos"},
	{"/assets/{path...}", "/assets/*", "/assets/*"},
}

var targets = map[string]string{
	"Static": "/v1/search/repos",
	"Param":  "/v1/users/42",
	"Deep":   "/v1/orgs/acme/repos/router/issues/17/comments",
}

type appContext struct {
	router.Base
}

type nopWriter struct{ h http.Header }

func (w *nopWriter) Header() http.Header         { return w.h }
func (w *nopWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *nopWriter) WriteHeader(int)             {}

func newOurs() http.Handler {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	for _, rt := range routes {
		r.GET(rt.ours, func(c *appContext) error { return c.NoContent(http.StatusOK) })
	}
	return r
}

func newOursPooled() http.Handler {
	r := router.NewPooled(
		func() *appContext { return new(appContext) },
		func(c *appContext) {},
	)
	for _, rt := range routes {
		r.GET(rt.ours, func(c *appContext) error { return c.NoContent(http.StatusOK) })
	}
	return r
}

func newChi() http.Handler {
	r := chi.NewRouter()
	for _, rt := range routes {
		r.Get(rt.chi, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	}
	return r
}

func newEcho() http.Handler {
	e := echo.New()
	for _, rt := range routes {
		e.GET(rt.echo, func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	}
	return e
}

func newStdlib() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range routes {
		mux.HandleFunc("GET "+rt.ours, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	}
	return mux
}

func run(b *testing.B, h http.Handler, target string) {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := &nopWriter{h: make(http.Header)}
	h.ServeHTTP(w, req)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		h.ServeHTTP(w, req)
	}
}

func BenchmarkRouters(b *testing.B) {
	impls := []struct {
		name       string
		newHandler func() http.Handler
	}{
		{"go-router", newOurs},
		{"go-router-pooled", newOursPooled},
		{"chi", newChi},
		{"echo", newEcho},
		{"stdlib", newStdlib},
	}
	for _, kind := range []string{"Static", "Param", "Deep"} {
		for _, impl := range impls {
			b.Run(kind+"/"+impl.name, func(b *testing.B) {
				run(b, impl.newHandler(), targets[kind])
			})
		}
	}
}
