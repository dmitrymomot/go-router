package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// follow sends the request and reports the status and Location of the answer.
func follow(h http.Handler, method, host, target string) (int, string) {
	req := httptest.NewRequest(method, target, nil)
	if host != "" {
		req.Host = host
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Header().Get(HeaderLocation)
}

func TestRedirectEscapesTheValue(t *testing.T) {
	r := newTestRouter()
	r.Redirect("/desk/{agent}", "/agents/{agent}?from={agent}", http.StatusFound)
	r.GET("/agents/{agent}", func(c *tctx) error {
		return c.String(http.StatusOK, c.Param("agent")+" from="+c.Query("from"))
	})

	for _, tc := range []struct{ raw, value, location string }{
		{"a%2Fb", "a/b", "/agents/a%2Fb?from=a%2Fb"},
		{"a%3Fb", "a?b", "/agents/a%3Fb?from=a%3Fb"},
		{"a%20b", "a b", "/agents/a%20b?from=a+b"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			code, loc := follow(r, http.MethodGet, "", "/desk/"+tc.raw)
			if code != http.StatusFound || loc != tc.location {
				t.Fatalf("GET /desk/%s = %d to %q, want 302 to %q", tc.raw, code, loc, tc.location)
			}
			if got := do(r, http.MethodGet, loc).Body.String(); got != tc.value+" from="+tc.value {
				t.Errorf("GET %s reached %q, want the value %q in both", loc, got, tc.value)
			}
		})
	}
}

// The router binds "/old//evil.com" into {p...} as "/evil.com", and a Location
// that starts with "//" is a link to another host.
func TestRedirectNeverPointsAtAnotherHost(t *testing.T) {
	captureLogs(t)
	r := newTestRouter()
	r.Redirect("/old/{p...}", "/{p...}", http.StatusMovedPermanently)

	for _, target := range []string{"/old//evil.com", "/old/%2Fevil.com", "/old//evil.com/x?y=1", "/old/%2F%2Fevil.com"} {
		code, loc := follow(r, http.MethodGet, "", target)
		if code != http.StatusNotFound {
			t.Errorf("GET %s = %d to %q, want 404", target, code, loc)
		}
		if strings.HasPrefix(loc, "//") {
			t.Errorf("GET %s points at %q, another host", target, loc)
		}
	}
	if code, loc := follow(r, http.MethodGet, "", "/old/docs/a.txt"); code != http.StatusMovedPermanently || loc != "/docs/a.txt" {
		t.Errorf("GET /old/docs/a.txt = %d to %q, want 301 to /docs/a.txt", code, loc)
	}
}

func TestRedirectKeepsTheRequestQuery(t *testing.T) {
	r := newTestRouter()
	r.Redirect("/old", "/new", http.StatusMovedPermanently)
	r.Redirect("/bonuses/{agent}", "/cashbox?agent={agent}", http.StatusFound)
	r.GET("/cashbox", func(c *tctx) error { return c.String(http.StatusOK, c.Query("agent")) })

	tests := []struct {
		target, want string
	}{
		{"/old?page=2", "/new?page=2"},
		{"/old", "/new"},
		{"/bonuses/a?page=2&agent=evil", "/cashbox?agent=a&page=2&agent=evil"},
		{"/bonuses/a", "/cashbox?agent=a"},
	}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			if _, loc := follow(r, http.MethodGet, "", tc.target); loc != tc.want {
				t.Errorf("GET %s points at %q, want %q", tc.target, loc, tc.want)
			}
		})
	}
	if got := do(r, http.MethodGet, "/cashbox?agent=a&page=2&agent=evil").Body.String(); got != "a" {
		t.Errorf("Query(agent) = %q, want the target's value", got)
	}
}

func TestRedirectAnswersGETAndHEADOnly(t *testing.T) {
	r := newTestRouter()
	r.Redirect("/old", "/new", http.StatusMovedPermanently)

	if code, loc := follow(r, http.MethodHead, "", "/old"); code != http.StatusMovedPermanently || loc != "/new" {
		t.Errorf("HEAD /old = %d to %q, want 301 to /new", code, loc)
	}
	rec := do(r, http.MethodPost, "/old")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /old = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get(HeaderAllow); got != "GET, HEAD, OPTIONS" {
		t.Errorf("Allow = %q, want %q", got, "GET, HEAD, OPTIONS")
	}
	want := []Route{{Method: http.MethodGet, Pattern: "/old"}}
	if got := r.Routes(); len(got) != 1 || got[0].Method != want[0].Method || got[0].Pattern != want[0].Pattern {
		t.Errorf("Routes() = %v, want %v", got, want)
	}
}

func TestRedirectTakesPrefixAndHostParams(t *testing.T) {
	r := newTestRouter()
	r.Host("{tenant}.example.com", func(h *Router[*tctx]) {
		h.Route("/t/{team}", func(g *Router[*tctx]) {
			g.Redirect("/old/{id}", "/{tenant}/{team}/items/{id}", http.StatusMovedPermanently)
		})
	})

	if _, loc := follow(r, http.MethodGet, "acme.example.com", "/t/red/old/7"); loc != "/acme/red/items/7" {
		t.Errorf("Location = %q, want %q", loc, "/acme/red/items/7")
	}
}

func TestRedirectTargetIsNotJoinedToTheScopePrefix(t *testing.T) {
	r := newTestRouter()
	r.Route("/api", func(g *Router[*tctx]) {
		g.Redirect("/v1/users", "/api/v2/users", http.StatusPermanentRedirect)
	})
	sub := newTestRouter()
	sub.Redirect("/old", "/new", http.StatusFound)
	r.Mount("/mounted", sub)

	if code, loc := follow(r, http.MethodGet, "", "/api/v1/users"); code != http.StatusPermanentRedirect || loc != "/api/v2/users" {
		t.Errorf("GET /api/v1/users = %d to %q, want 308 to /api/v2/users", code, loc)
	}
	if _, loc := follow(r, http.MethodGet, "", "/mounted/old"); loc != "/new" {
		t.Errorf("GET /mounted/old points at %q, want /new", loc)
	}
}

func TestRedirectMovesACatchAll(t *testing.T) {
	r := newTestRouter()
	r.Redirect("/docs/{path...}", "/manual/{path...}", http.StatusMovedPermanently)

	tests := []struct{ target, want string }{
		{"/docs/a/b c.txt", "/manual/a/b%20c.txt"},
		{"/docs", "/manual"},
		{"/docs/", "/manual"},
	}
	for _, tc := range tests {
		if _, loc := follow(r, http.MethodGet, "", strings.ReplaceAll(tc.target, " ", "%20")); loc != tc.want {
			t.Errorf("GET %s points at %q, want %q", tc.target, loc, tc.want)
		}
	}
}

func TestRedirectChecksDeclaredClasses(t *testing.T) {
	captureLogs(t)
	r := newTestRouter()
	r.ParamClass("shortid", func(s string) bool { return len(s) == 6 })
	r.Redirect("/s/{code}", "/short/{code:shortid}", http.StatusFound)

	if code, loc := follow(r, http.MethodGet, "", "/s/abc123"); code != http.StatusFound || loc != "/short/abc123" {
		t.Errorf("GET /s/abc123 = %d to %q, want 302 to /short/abc123", code, loc)
	}
	if code, _ := follow(r, http.MethodGet, "", "/s/abc"); code != http.StatusNotFound {
		t.Errorf("GET /s/abc = %d, want 404; the class refuses the value", code)
	}

	defer func() {
		if msg := fmt.Sprint(recover()); !strings.Contains(msg, "names no parameter class") {
			t.Errorf("panic = %q, want one about the undeclared class", msg)
		}
	}()
	r.Redirect("/n/{code}", "/new/{code:nope}", http.StatusFound)
}

func TestRedirectAnswers404WhenTheTargetRefusesTheValue(t *testing.T) {
	captureLogs(t)
	r := newTestRouter()
	r.Redirect("/old/{id}", "/new/{id:int}", http.StatusMovedPermanently)
	r.Redirect("/x/{env}/{name}", "/r/{env}-{name}", http.StatusMovedPermanently)

	for _, target := range []string{"/old/abc", "/x/prod/web-api"} {
		if code, loc := follow(r, http.MethodGet, "", target); code != http.StatusNotFound {
			t.Errorf("GET %s = %d to %q, want 404", target, code, loc)
		}
	}
	if code, loc := follow(r, http.MethodGet, "", "/old/42"); code != http.StatusMovedPermanently || loc != "/new/42" {
		t.Errorf("GET /old/42 = %d to %q, want 301 to /new/42", code, loc)
	}
}

func TestRedirectRunsScopeMiddleware(t *testing.T) {
	r := newTestRouter()
	r.Use(setHeader("X-Root", "root"))
	r.With(setHeader("X-With", "with")).Redirect("/old", "/new", http.StatusFound)

	rec := do(r, http.MethodGet, "/old")
	if rec.Header().Get("X-Root") != "root" || rec.Header().Get("X-With") != "with" {
		t.Errorf("headers = %v, want the middleware of every scope around the redirect", rec.Header())
	}
}

func TestRedirectPanicsOnABadCall(t *testing.T) {
	tests := []struct {
		name string
		call func(r *Router[*tctx])
		want string
	}{
		{"status 200", func(r *Router[*tctx]) { r.Redirect("/a", "/b", http.StatusOK) }, "redirect status"},
		{"status 304", func(r *Router[*tctx]) { r.Redirect("/a", "/b", http.StatusNotModified) }, "redirect status"},
		{"status 0", func(r *Router[*tctx]) { r.Redirect("/a", "/b", 0) }, "redirect status"},
		{"a target that is not a path", func(r *Router[*tctx]) { r.Redirect("/a", "cashbox", http.StatusFound) }, `starts with "/"`},
		{"an absolute URL", func(r *Router[*tctx]) { r.Redirect("/a", "https://example.com/", http.StatusFound) }, `starts with "/"`},
		{"a parameter the route lacks", func(r *Router[*tctx]) { r.Redirect("/x/{id}", "/x/{nope}", http.StatusFound) }, `"nope"`},
		{"a malformed target", func(r *Router[*tctx]) { r.Redirect("/x/{id}", "/x/{", http.StatusFound) }, "unbalanced"},
		{"a malformed route", func(r *Router[*tctx]) { r.Redirect("/x/{", "/y", http.StatusFound) }, "unbalanced"},
		{"a target equal to the route", func(r *Router[*tctx]) { r.Redirect("/a", "/a/", http.StatusFound) }, "points at itself"},
		{"a target equal to the route under a prefix", func(r *Router[*tctx]) {
			r.Route("/p", func(g *Router[*tctx]) { g.Redirect("/a", "/p/a", http.StatusFound) })
		}, "points at itself"},
		{"a parameter one host lacks", func(r *Router[*tctx]) {
			r.Hosts([]string{"{tenant}.example.com", "example.com"}, func(h *Router[*tctx]) {
				h.Redirect("/old", "/{tenant}", http.StatusFound)
			})
		}, `"tenant"`},
		{"a route already registered", func(r *Router[*tctx]) {
			r.GET("/a", echoRoute)
			r.Redirect("/a", "/b", http.StatusFound)
		}, "already registered"},
		{"after serving", func(r *Router[*tctx]) {
			do(r, http.MethodGet, "/")
			r.Redirect("/a", "/b", http.StatusFound)
		}, "after the router started serving"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if msg := fmt.Sprint(recover()); !strings.Contains(msg, tc.want) {
					t.Errorf("panic = %q, want one that mentions %q", msg, tc.want)
				}
			}()
			tc.call(newTestRouter())
		})
	}
}
