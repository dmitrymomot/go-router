package benchmarks

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/labstack/echo/v5"

	"github.com/dmitrymomot/go-router"
)

// impl builds one router from a route table. reads names the parameters the
// handler asks for, so a case can price parameter access as well as the match.
type impl struct {
	name  string
	build func(table []benchRoute, reads []string) http.Handler
}

type benchRoute struct{ method, pattern string }

// echoPath rewrites "{name}" as echo's ":name". No table below uses a
// catch-all, so this is the only syntax the three route languages differ on.
func echoPath(p string) string {
	return strings.NewReplacer("{", ":", "}", "").Replace(p)
}

var impls = []impl{
	{"go-router", func(table []benchRoute, reads []string) http.Handler {
		r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
		addOurs(r, table, reads)
		return r
	}},
	{"go-router-pooled", func(table []benchRoute, reads []string) http.Handler {
		r := router.NewPooled(func() *appContext { return new(appContext) }, func(c *appContext) {})
		addOurs(r, table, reads)
		return r
	}},
	{"chi", func(table []benchRoute, reads []string) http.Handler {
		r := chi.NewRouter()
		h := func(w http.ResponseWriter, req *http.Request) {
			for _, name := range reads {
				_ = chi.URLParam(req, name)
			}
			w.WriteHeader(http.StatusOK)
		}
		for _, rt := range table {
			r.Method(rt.method, rt.pattern, http.HandlerFunc(h))
		}
		return r
	}},
	{"echo", func(table []benchRoute, reads []string) http.Handler {
		e := echo.New()
		h := func(c *echo.Context) error {
			for _, name := range reads {
				_ = c.Param(name)
			}
			return c.NoContent(http.StatusOK)
		}
		for _, rt := range table {
			e.Add(rt.method, echoPath(rt.pattern), h)
		}
		return e
	}},
	{"stdlib", func(table []benchRoute, reads []string) http.Handler {
		mux := http.NewServeMux()
		h := func(w http.ResponseWriter, req *http.Request) {
			for _, name := range reads {
				_ = req.PathValue(name)
			}
			w.WriteHeader(http.StatusOK)
		}
		for _, rt := range table {
			mux.HandleFunc(rt.method+" "+rt.pattern, h)
		}
		return mux
	}},
}

func addOurs(r *router.Router[*appContext], table []benchRoute, reads []string) {
	h := func(c *appContext) error {
		for _, name := range reads {
			_ = c.Param(name)
		}
		return c.NoContent(http.StatusOK)
	}
	for _, rt := range table {
		r.Handle(rt.method, rt.pattern, h)
	}
}

func runCase(b *testing.B, table []benchRoute, reads []string, target string) {
	for _, im := range impls {
		b.Run(im.name, func(b *testing.B) {
			run(b, im.build(table, reads), target)
		})
	}
}

// BenchmarkParamAccess prices the parameter read that BenchmarkRouters leaves
// out. chi and http.ServeMux defer the lookup to URLParam and PathValue, so a
// handler that never asks for a parameter never pays for one.
func BenchmarkParamAccess(b *testing.B) {
	table := []benchRoute{
		{http.MethodGet, "/v1/users/{id}"},
		{http.MethodGet, "/v1/orgs/{org}/repos/{repo}/issues/{num}/comments"},
	}
	b.Run("One", func(b *testing.B) {
		runCase(b, table, []string{"id"}, "/v1/users/42")
	})
	b.Run("Three", func(b *testing.B) {
		runCase(b, table, []string{"org", "repo", "num"},
			"/v1/orgs/acme/repos/router/issues/17/comments")
	})
}

// BenchmarkFiveParams crosses the four-slot inline parameter array, which
// costs the router one allocation for the overflow.
func BenchmarkFiveParams(b *testing.B) {
	table := []benchRoute{{http.MethodGet, "/{a}/{b}/{c}/{d}/{e}"}}
	runCase(b, table, []string{"a", "b", "c", "d", "e"}, "/1/2/3/4/5")
}

// TestCasesMatch proves every router answers every benchmarked target, so no
// measurement below prices a miss the case did not ask for.
func TestCasesMatch(t *testing.T) {
	cases := []struct {
		name   string
		table  []benchRoute
		target string
		want   int
	}{
		{"param-one", []benchRoute{{http.MethodGet, "/v1/users/{id}"}}, "/v1/users/42", http.StatusOK},
		{"five-params", []benchRoute{{http.MethodGet, "/{a}/{b}/{c}/{d}/{e}"}}, "/1/2/3/4/5", http.StatusOK},
		{"scale-static", githubRoutes, "/repositories", http.StatusOK},
		{"scale-param", githubRoutes, "/repos/acme/router/pulls/42/comments", http.StatusOK},
		{"scale-notfound", githubRoutes, "/nothing/here/at/all", http.StatusNotFound},
	}
	for _, c := range cases {
		for _, im := range impls {
			rec := httptest.NewRecorder()
			im.build(c.table, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.target, nil))
			if rec.Code != c.want {
				t.Errorf("%s/%s: status = %d, want %d", c.name, im.name, rec.Code, c.want)
			}
		}
	}
}

// BenchmarkScale runs the same three shapes against a 184-route table over
// four methods, where the trie is deep enough for its shape to matter.
func BenchmarkScale(b *testing.B) {
	b.Run("Static", func(b *testing.B) {
		runCase(b, githubRoutes, nil, "/repositories")
	})
	b.Run("Param", func(b *testing.B) {
		runCase(b, githubRoutes, []string{"owner", "repo", "number"},
			"/repos/acme/router/pulls/42/comments")
	})
	b.Run("NotFound", func(b *testing.B) {
		runCase(b, githubRoutes, nil, "/nothing/here/at/all")
	})
}
