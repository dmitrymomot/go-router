package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// nopWriter discards the answer, so a benchmark measures the router and not
// the recorder.
type nopWriter struct{ h http.Header }

func (w *nopWriter) Header() http.Header         { return w.h }
func (w *nopWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *nopWriter) WriteHeader(int)             {}

func benchRouter(patterns ...string) (*Router[*tctx], *nopWriter) {
	r := New(func(http.ResponseWriter, *http.Request) *tctx { return new(tctx) })
	for _, p := range patterns {
		r.GET(p, func(c *tctx) error { return c.NoContent(http.StatusOK) })
	}
	return r, &nopWriter{h: make(http.Header)}
}

func benchServe(b *testing.B, r http.Handler, w http.ResponseWriter, target string) {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		r.ServeHTTP(w, req)
	}
}

func BenchmarkStatic(b *testing.B) {
	r, w := benchRouter("/", "/users", "/users/settings", "/health")
	benchServe(b, r, w, "/users/settings")
}

func BenchmarkOneParam(b *testing.B) {
	r, w := benchRouter("/users/{id}")
	benchServe(b, r, w, "/users/42")
}

func BenchmarkFiveParams(b *testing.B) {
	r, w := benchRouter("/{a}/{b}/{c}/{d}/{e}")
	benchServe(b, r, w, "/1/2/3/4/5")
}

func BenchmarkCatchAll(b *testing.B) {
	r, w := benchRouter("/assets/{path...}")
	benchServe(b, r, w, "/assets/css/site/app.css")
}

func BenchmarkRegexParam(b *testing.B) {
	r, w := benchRouter("/orders/{id:[0-9]+}")
	benchServe(b, r, w, "/orders/123456")
}

func BenchmarkBacktrack(b *testing.B) {
	// The static branch /a/b exists but has no c below it, so the walk has to
	// fall back to the parameter branch.
	r, w := benchRouter("/a/{x}/c", "/a/b/d")
	benchServe(b, r, w, "/a/b/c")
}

func BenchmarkNotFound(b *testing.B) {
	r, w := benchRouter("/users/{id}")
	benchServe(b, r, w, "/nothing/here")
}

// apiRoutes is a route set of the size that a real service has.
var apiRoutes = []string{
	"/", "/health", "/metrics",
	"/v1/users", "/v1/users/{id}", "/v1/users/{id}/avatar",
	"/v1/users/{id}/sessions", "/v1/users/{id}/sessions/{sid}",
	"/v1/orgs", "/v1/orgs/{org}", "/v1/orgs/{org}/members",
	"/v1/orgs/{org}/members/{uid}", "/v1/orgs/{org}/repos",
	"/v1/orgs/{org}/repos/{repo}", "/v1/orgs/{org}/repos/{repo}/issues",
	"/v1/orgs/{org}/repos/{repo}/issues/{num}",
	"/v1/orgs/{org}/repos/{repo}/issues/{num}/comments",
	"/v1/orgs/{org}/repos/{repo}/commits/{sha}",
	"/v1/orgs/{org}/repos/{repo}/contents/{path...}",
	"/v1/search", "/v1/search/users", "/v1/search/repos",
	"/v2/billing/plans", "/v2/billing/plans/{plan}",
	"/v2/billing/invoices/{year}/{month}",
	"/assets/{path...}",
}

func BenchmarkAPIStatic(b *testing.B) {
	r, w := benchRouter(apiRoutes...)
	benchServe(b, r, w, "/v1/search/repos")
}

func BenchmarkAPIDeep(b *testing.B) {
	r, w := benchRouter(apiRoutes...)
	benchServe(b, r, w, "/v1/orgs/acme/repos/router/issues/17/comments")
}

func BenchmarkMiddlewareChain(b *testing.B) {
	pass := func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error { return next(c) }
	}
	r := New(func(http.ResponseWriter, *http.Request) *tctx { return new(tctx) })
	for range 8 {
		r.Use(pass)
	}
	r.GET("/users/{id}", func(c *tctx) error { return c.NoContent(http.StatusOK) })
	benchServe(b, r, &nopWriter{h: make(http.Header)}, "/users/42")
}

// benchHostRouter builds the four host shapes that a multi-tenant service
// needs, each with the same small route set.
func benchHostRouter() (*Router[*tctx], *nopWriter) {
	ok := func(c *tctx) error { return c.NoContent(http.StatusOK) }
	r := New(func(http.ResponseWriter, *http.Request) *tctx { return new(tctx) })
	r.Host("example.com", func(h *Router[*tctx]) {
		h.GET("/", ok)
		h.GET("/blog/{slug}", ok)
	})
	r.Host("api.example.com", func(h *Router[*tctx]) { h.GET("/v1/users/{id}", ok) })
	r.Hosts([]string{"{tenant}.example.com", "*"}, func(h *Router[*tctx]) {
		h.GET("/", ok)
		h.GET("/settings", ok)
	})
	r.GET("/healthz", ok)
	return r, &nopWriter{h: make(http.Header)}
}

func benchServeHost(b *testing.B, r http.Handler, w http.ResponseWriter, host, target string) {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = host
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		r.ServeHTTP(w, req)
	}
}

func BenchmarkHostExact(b *testing.B) {
	r, w := benchHostRouter()
	benchServeHost(b, r, w, "api.example.com", "/v1/users/42")
}

func BenchmarkHostParam(b *testing.B) {
	r, w := benchHostRouter()
	benchServeHost(b, r, w, "acme.example.com", "/settings")
}

func BenchmarkHostAny(b *testing.B) {
	r, w := benchHostRouter()
	benchServeHost(b, r, w, "acme.com", "/settings")
}

// BenchmarkHostFallback measures a route that no host scope registered, which
// costs the host lookup and then a second trie walk.
func BenchmarkHostFallback(b *testing.B) {
	r, w := benchHostRouter()
	benchServeHost(b, r, w, "api.example.com", "/healthz")
}
