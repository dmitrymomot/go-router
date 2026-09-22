package router

import (
	"errors"
	"net/http"
	"testing"
)

// These tests pin which error handler answers: a matched route goes to the
// nearest handler of the scopes that registered it, and a 404 or 405 goes to the
// handler of the most specific scope with a prefix or a host that covers the path.

func tagHandler(name string) ErrorHandlerFunc[*tctx] {
	return func(c *tctx, err error) error { return c.String(StatusOf(err), name) }
}

func failRoute(*tctx) error { return errors.New("boom") }

// answeredBy runs one request and reports the name of the handler that wrote it.
func answeredBy(t *testing.T, h http.Handler, method, host, target string) string {
	t.Helper()
	if host == "" {
		return do(h, method, target).Body.String()
	}
	return doHost(h, method, host, target).Body.String()
}

func wantOwners(t *testing.T, h http.Handler, cases [][4]string) {
	t.Helper()
	for _, tc := range cases {
		if got := answeredBy(t, h, tc[0], tc[1], tc[2]); got != tc[3] {
			t.Errorf("%s %s%s answered by %q, want %q", tc[0], tc[1], tc[2], got, tc[3])
		}
	}
}

func TestErrorHandlerOfARouterMountedAtTheRootLeavesTheParentAlone(t *testing.T) {
	r, sub := newTestRouter(), newTestRouter()
	r.ErrorHandler(tagHandler("root"))
	sub.ErrorHandler(tagHandler("sub"))
	sub.GET("/child", failRoute)
	r.Mount("/", sub)
	r.GET("/parent", failRoute)

	wantOwners(t, r, [][4]string{
		{http.MethodGet, "", "/parent", "root"},
		{http.MethodGet, "", "/nope", "root"},
		{http.MethodPost, "", "/parent", "root"},
		{http.MethodGet, "", "/child", "sub"},
	})
}

func TestMountedErrorHandlerLeavesAParentRouteUnderItsPrefixAlone(t *testing.T) {
	r, sub := newTestRouter(), newTestRouter()
	r.ErrorHandler(tagHandler("root"))
	sub.ErrorHandler(tagHandler("sub"))
	sub.GET("/child", failRoute)
	r.Mount("/api", sub)
	r.GET("/api/other", failRoute)

	wantOwners(t, r, [][4]string{
		{http.MethodGet, "", "/api/other", "root"},
		{http.MethodGet, "", "/api/child", "sub"},
		{http.MethodGet, "", "/api/nope", "sub"},
		{http.MethodGet, "", "/nope", "root"},
	})
}

func TestAGroupErrorHandlerOwnsItsRoutesAlone(t *testing.T) {
	r := newTestRouter()
	r.ErrorHandler(tagHandler("root"))
	r.Route("/api", func(api *Router[*tctx]) {
		api.GET("/a", failRoute)
		api.Group(func(g *Router[*tctx]) {
			g.ErrorHandler(tagHandler("group"))
			g.GET("/b", failRoute)
		})
	})

	wantOwners(t, r, [][4]string{
		{http.MethodGet, "", "/api/b", "group"},
		{http.MethodGet, "", "/api/a", "root"},
		{http.MethodGet, "", "/api/typo", "root"},
		{http.MethodPost, "", "/api/b", "root"},
	})
}

func TestAGroupAtTheRootMayOwnItsErrors(t *testing.T) {
	r := newTestRouter()
	r.ErrorHandler(tagHandler("root"))
	r.Group(func(g *Router[*tctx]) {
		g.ErrorHandler(tagHandler("group"))
		g.GET("/in", failRoute)
	})
	r.GET("/out", failRoute)

	wantOwners(t, r, [][4]string{
		{http.MethodGet, "", "/in", "group"},
		{http.MethodGet, "", "/out", "root"},
		{http.MethodGet, "", "/nope", "root"},
	})
}

func TestErrorHandlerSetAfterItsRoutesStillOwnsThem(t *testing.T) {
	r := newTestRouter()
	api := r.Route("/api", nil)
	api.GET("/early", failRoute)
	api.With(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] { return next }).GET("/with", failRoute)
	api.ErrorHandler(tagHandler("api"))

	wantOwners(t, r, [][4]string{
		{http.MethodGet, "", "/api/early", "api"},
		{http.MethodGet, "", "/api/with", "api"},
		{http.MethodGet, "", "/api/nope", "api"},
	})
}

func TestNearestScopeErrorHandlerWins(t *testing.T) {
	r := newTestRouter()
	r.Route("/api", func(api *Router[*tctx]) {
		api.ErrorHandler(tagHandler("A"))
		api.Route("/v1", func(v1 *Router[*tctx]) {
			v1.Group(func(g *Router[*tctx]) {
				g.ErrorHandler(tagHandler("B"))
				g.GET("/x", failRoute)
			})
			v1.GET("/y", failRoute)
		})
	})

	wantOwners(t, r, [][4]string{
		{http.MethodGet, "", "/api/v1/x", "B"},
		{http.MethodGet, "", "/api/v1/y", "A"},
		{http.MethodGet, "", "/api/v1/nope", "A"},
	})
}

func TestAMatchedRouteIgnoresAPrefixScopeItIsNotIn(t *testing.T) {
	r := newTestRouter()
	r.ErrorHandler(tagHandler("root"))
	r.Route("/api", func(api *Router[*tctx]) {
		api.ErrorHandler(tagHandler("A"))
		api.GET("/in", failRoute)
	})
	r.GET("/api/raw", failRoute)

	wantOwners(t, r, [][4]string{
		{http.MethodGet, "", "/api/raw", "root"},
		{http.MethodGet, "", "/api/in", "A"},
	})
}

func TestASecondErrorHandlerForAHostPanics(t *testing.T) {
	t.Run("two Host scopes", func(t *testing.T) {
		r := newTestRouter()
		r.Host("a.test", func(h *Router[*tctx]) { h.ErrorHandler(tagHandler("first")) })
		mustPanicContaining(t, "already has an error handler", func() {
			r.Host("a.test", func(h *Router[*tctx]) { h.ErrorHandler(tagHandler("second")) })
		})
	})
	t.Run("Hosts overlap", func(t *testing.T) {
		r := newTestRouter()
		r.Hosts([]string{"a.test", "b.test"}, func(h *Router[*tctx]) { h.ErrorHandler(tagHandler("ab")) })
		mustPanicContaining(t, "already has an error handler", func() {
			r.Host("b.test", func(h *Router[*tctx]) { h.ErrorHandler(tagHandler("b")) })
		})
	})
	t.Run("Mount", func(t *testing.T) {
		r, sub := newTestRouter(), newTestRouter()
		r.Host("a.test", func(h *Router[*tctx]) { h.ErrorHandler(tagHandler("parent")) })
		sub.Host("a.test", func(h *Router[*tctx]) { h.ErrorHandler(tagHandler("sub")) })
		mustPanicContaining(t, "already has an error handler", func() { r.Mount("/m", sub) })
	})
	t.Run("the same scope twice", func(t *testing.T) {
		r := newTestRouter()
		h := r.Host("a.test", nil)
		h.ErrorHandler(tagHandler("first"))
		h.ErrorHandler(tagHandler("second"))
		h.GET("/x", failRoute)
		wantOwners(t, r, [][4]string{{http.MethodGet, "a.test", "/x", "second"}})
	})
}

func TestTheHostErrorHandlerCoversEveryScopeOfItsPattern(t *testing.T) {
	r := newTestRouter()
	r.ErrorHandler(tagHandler("root"))
	r.Host("a.test", func(h *Router[*tctx]) { h.GET("/one", failRoute) })
	r.Host("a.test", func(h *Router[*tctx]) {
		h.ErrorHandler(tagHandler("second"))
		h.GET("/two", failRoute)
	})

	wantOwners(t, r, [][4]string{
		{http.MethodGet, "a.test", "/one", "second"},
		{http.MethodGet, "a.test", "/two", "second"},
		{http.MethodGet, "a.test", "/none", "second"},
		{http.MethodGet, "b.test", "/none", "root"},
	})
}

func TestAnErrorHandlerAboveAHostScope(t *testing.T) {
	r := newTestRouter()
	r.ErrorHandler(tagHandler("root"))
	r.Group(func(g *Router[*tctx]) {
		g.ErrorHandler(tagHandler("group"))
		g.Host("a.test", func(h *Router[*tctx]) { h.GET("/x", failRoute) })
		g.Host("b.test", func(h *Router[*tctx]) {
			h.ErrorHandler(tagHandler("b"))
			h.GET("/x", failRoute)
		})
	})

	wantOwners(t, r, [][4]string{
		{http.MethodGet, "a.test", "/x", "group"},
		{http.MethodGet, "b.test", "/x", "b"},
		{http.MethodGet, "b.test", "/none", "b"},
	})
}

func TestErrorOwnerForHEADAnyAndMountHandlerRoutes(t *testing.T) {
	r := newTestRouter()
	r.ErrorHandler(tagHandler("root"))
	r.Route("/s", func(s *Router[*tctx]) {
		s.ErrorHandler(tagHandler("s"))
		s.GET("/get", failRoute)
		s.Any("/any", failRoute)
		s.MountHandler("/std", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			panic("boom")
		}))
	})

	wantOwners(t, r, [][4]string{
		{http.MethodGet, "", "/s/get", "s"},
		{http.MethodPatch, "", "/s/any", "s"},
		{http.MethodGet, "", "/s/std/x", "s"},
	})
	if rec := do(r, http.MethodHead, "/s/get"); rec.Code != http.StatusInternalServerError {
		t.Errorf("HEAD /s/get = %d, want 500 from the scope handler", rec.Code)
	}
}

func TestASubMountedIntoTwoParentsKeepsEachParentsOwners(t *testing.T) {
	sub := newTestRouter()
	sub.GET("/x", failRoute)
	left, right := newTestRouter(), newTestRouter()
	left.Route("/l", func(g *Router[*tctx]) {
		g.ErrorHandler(tagHandler("left"))
		g.Mount("/m", sub)
	})
	right.Route("/r", func(g *Router[*tctx]) {
		g.ErrorHandler(tagHandler("right"))
		g.Mount("/m", sub)
	})

	wantOwners(t, left, [][4]string{{http.MethodGet, "", "/l/m/x", "left"}})
	wantOwners(t, right, [][4]string{{http.MethodGet, "", "/r/m/x", "right"}})
}
