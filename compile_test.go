package router

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// answering returns an error handler that names its scope, so a probe shows
// which scope answered.
func answering(name string) ErrorHandlerFunc[*tctx] {
	return func(c *tctx, err error) error { return c.String(StatusOf(err), name+" "+c.RoutePattern()) }
}

// buildIncrementalTable opens scopes one at a time in every shape a compile
// of one subtree has to get right: nested prefixes, a Group around them, host
// scopes old and new, a prefix repeated at one depth, a mount, a handler set
// on a scope after its first compile, and parameters in the prefixes.
func buildIncrementalTable() *Router[*tctx] {
	r := newTestRouter()
	r.Use(setHeader("X-Root", "1"))
	r.ErrorHandler(answering("root"))
	r.GET("/", echoRoute)
	r.Route("/api", func(api *Router[*tctx]) {
		api.ErrorHandler(answering("api"))
		api.GET("/users/{id}", echoRoute)
		api.Route("/v2", func(v2 *Router[*tctx]) { v2.GET("/x", echoRoute) })
	})
	r.Route("/api/v2", func(g *Router[*tctx]) {
		g.Use(setHeader("X-Flat", "1"))
		g.POST("/y", echoRoute)
	})
	g := r.Group(func(g *Router[*tctx]) {
		g.Route("/g1", func(s *Router[*tctx]) { s.GET("/a", echoRoute) })
		g.Route("/g2/{id}", func(s *Router[*tctx]) {
			s.ErrorHandler(answering("g2"))
			s.GET("/a", echoRoute)
		})
	})
	g.Route("/g3", func(s *Router[*tctx]) { s.PUT("/", echoRoute) })
	r.Host("a.example.com", func(h *Router[*tctx]) {
		h.ErrorHandler(answering("host a"))
		h.GET("/", echoRoute)
		h.Route("/in", func(s *Router[*tctx]) { s.GET("/x", echoRoute) })
	})
	h := r.Host("{t}.example.com", func(h *Router[*tctx]) { h.GET("/t", echoRoute) })
	h.Route("/late", func(s *Router[*tctx]) {
		s.ErrorHandler(answering("late"))
		s.GET("/{n:int}", echoRoute)
	})
	r.Host("a.example.com", func(h *Router[*tctx]) {
		h.Route("/again", func(s *Router[*tctx]) { s.GET("/z", echoRoute) })
	})
	sub := newTestRouter()
	sub.ErrorHandler(answering("sub"))
	sub.GET("/s/{id}", echoRoute)
	sub.Route("/deep", func(s *Router[*tctx]) { s.GET("/d", echoRoute) })
	r.Mount("/mnt/{tenant}", sub)
	for i := range 20 {
		r.Route(fmt.Sprintf("/r%d/{id}", i), func(s *Router[*tctx]) {
			if i%3 == 0 {
				s.ErrorHandler(answering(fmt.Sprint("r", i)))
			}
			s.GET("/", echoRoute)
		})
	}
	w := r.With(setHeader("X-With", "1"))
	w.Route("/with", func(s *Router[*tctx]) { s.GET("/w", echoRoute) })
	late := r.Route("/later", nil)
	late.GET("/a", echoRoute)
	late.ErrorHandler(answering("later"))
	return r
}

func TestIncrementalCompileMatchesAFullCompile(t *testing.T) {
	inc := buildIncrementalTable()
	full := buildIncrementalTable()
	full.compile()

	probes := []struct{ method, host, path string }{}
	paths := []string{
		"/", "/missing", "/api", "/api/missing", "/api/users/1", "/api/users/1/x", "/api/v2", "/api/v2/x",
		"/api/v2/y", "/api/v2/missing", "/g1/a", "/g1/b", "/g2/7/a", "/g2/7/b", "/g3", "/g3/x", "/in/x", "/in/y",
		"/t", "/late/5", "/late/x", "/again/z", "/again/q", "/mnt/acme/s/1", "/mnt/acme/x", "/mnt/acme/deep/q",
		"/r0/1", "/r0/1/x", "/r4/1/x", "/r18/2/q", "/with/w", "/with/x", "/later/a", "/later/b",
	}
	for _, host := range []string{"example.org", "a.example.com", "b.example.com"} {
		for _, p := range paths {
			for _, m := range []string{http.MethodGet, http.MethodDelete, http.MethodOptions} {
				probes = append(probes, struct{ method, host, path string }{m, host, p})
			}
		}
	}
	for _, p := range probes {
		got, want := probe(inc, p.method, p.host, p.path), probe(full, p.method, p.host, p.path)
		if got != want {
			t.Errorf("%s %s%s:\n incremental %s\n full        %s", p.method, p.host, p.path, got, want)
		}
	}
}

func probe(h http.Handler, method, host, path string) string {
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	hd := rec.Header()
	return fmt.Sprintf("%d %q allow=%q root=%q flat=%q with=%q",
		rec.Code, rec.Body.String(), hd.Get(HeaderAllow), hd.Get("X-Root"), hd.Get("X-Flat"), hd.Get("X-With"))
}

// A prefix repeated at one depth goes to the scope the walk sees first, which
// a compile of the later subtree alone cannot tell, so it compiles the graph.
func TestARepeatedPrefixKeepsTheFirstScope(t *testing.T) {
	r := newTestRouter()
	scopeAnswering(r, "/a")
	scopeAnswering(r, "/a")
	if rec := do(r, http.MethodGet, "/a/missing"); rec.Body.String() != "/a" || rec.Code != http.StatusNotFound {
		t.Fatalf("GET /a/missing = %d %q", rec.Code, rec.Body.String())
	}
	r = newTestRouter()
	g := r.Group(nil)
	r.Route("/b", func(s *Router[*tctx]) { s.ErrorHandler(answering("second")) })
	// Opened later, but walked first: the Group sits before /b.
	g.Route("/b", func(s *Router[*tctx]) { s.ErrorHandler(answering("first")) })
	if rec := do(r, http.MethodGet, "/b/missing"); rec.Body.String() != "first " {
		t.Errorf("GET /b/missing = %q, want the scope that the walk sees first", rec.Body.String())
	}
}

// coveringScope is the linear scan the index replaced: every scope in the
// order compareScopes sorts them, the first that covers the path.
func coveringScope(scopes []*scopeFallback[*tctx], path string) *scopeFallback[*tctx] {
	sorted := slices.Clone(scopes)
	slices.SortStableFunc(sorted, compareScopes[*tctx])
	for _, s := range sorted {
		if s.covers(path, false) {
			return s
		}
	}
	return nil
}

func TestScopeIndexMatchesALinearScan(t *testing.T) {
	segs := []string{"a", "b", "{p}", "{q:int}", "{r:[a-c]+}", "x-{t}", "x-{u}-y", "{w...}"}
	paths := []string{"a", "b", "7", "c", "x-1", "x-1-y", "zz", ""}
	rng := rand.New(rand.NewPCG(1, 2))
	for round := range 200 {
		idx := new(scopeNode[*tctx])
		var scopes []*scopeFallback[*tctx]
		for range 1 + rng.IntN(12) {
			var b strings.Builder
			for n := 1 + rng.IntN(3); n > 0; n-- {
				seg := segs[rng.IntN(len(segs))]
				b.WriteString("/" + seg)
				if strings.HasSuffix(seg, "...}") {
					break
				}
			}
			ps, _, err := parsePattern(b.String(), builtinClass)
			if err != nil {
				continue
			}
			s := &scopeFallback[*tctx]{prefix: b.String(), pattern: ps, depth: rng.IntN(3)}
			scopes = append(scopes, s)
			idx.add(s, false)
		}
		for range 30 {
			var b strings.Builder
			for n := rng.IntN(4); n >= 0; n-- {
				b.WriteString("/" + paths[rng.IntN(len(paths))])
			}
			path := b.String()
			got, want := idx.find(path, false), coveringScope(scopes, path)
			if got != want {
				t.Fatalf("round %d, path %q: index found %v, scan found %v", round, path, describe(got), describe(want))
			}
		}
	}
}

func describe(s *scopeFallback[*tctx]) string {
	if s == nil {
		return "none"
	}
	return fmt.Sprintf("%s@%d", s.prefix, s.depth)
}
