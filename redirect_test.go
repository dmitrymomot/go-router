package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestRedirectRunsScopeMiddlewareAndCarriesMeta(t *testing.T) {
	r := newTestRouter()
	r.Use(setHeader("X-Root", "root"), func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			if p, ok := MetaAs[perm](c); ok {
				c.Response().Header().Set("X-Perm", string(p))
			}
			return next(c)
		}
	})
	r.With(setHeader("X-With", "with")).Meta(perm("legacy")).Redirect("/old", "/new", http.StatusFound)

	rec := do(r, http.MethodGet, "/old")
	if rec.Header().Get("X-Root") != "root" || rec.Header().Get("X-With") != "with" || rec.Header().Get("X-Perm") != "legacy" {
		t.Errorf("headers = %v, want the middleware of every scope around the redirect, and its Meta", rec.Header())
	}
	if got := r.Routes()[0].Meta; !reflect.DeepEqual(got, []any{perm("legacy")}) {
		t.Errorf("Routes()[0].Meta = %v, want [perm legacy]", got)
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

func TestRedirectHostCoversEveryPathOnAnUnknownHost(t *testing.T) {
	r := newTestRouter()
	r.Host("example.com", func(h *Router[*tctx]) { h.GET("/", echoHost) })
	r.RedirectHost("*", "example.com", http.StatusMovedPermanently)
	r.GET("/healthz", echoHost)

	for _, target := range []string{"/", "/healthz", "/a/b?c=d"} {
		code, loc := follow(r, http.MethodGet, "other.test", target)
		if code != http.StatusMovedPermanently || loc != "http://example.com"+target {
			t.Errorf("GET other.test%s = %d to %q, want 301 to http://example.com%s", target, code, loc, target)
		}
	}
	if rec := doHost(r, http.MethodGet, "example.com", "/"); rec.Code != http.StatusOK {
		t.Errorf("GET example.com/ = %d, want 200", rec.Code)
	}
}

func TestRedirectHostKeepsPathQueryPortAndScheme(t *testing.T) {
	r := newTestRouter()
	r.RedirectHost("www.example.com", "example.com", http.StatusMovedPermanently)
	r.RedirectHost("{tenant}.old.test", "{tenant}.new.test", http.StatusPermanentRedirect)
	r.RedirectHost("legacy.test", "example.com:8443", http.StatusFound)

	tests := []struct {
		name, method, host, target, proto string
		code                              int
		want                              string
	}{
		{"plain", http.MethodGet, "www.example.com", "/pricing?plan=pro", "", 301, "http://example.com/pricing?plan=pro"},
		{"a port", http.MethodGet, "www.example.com:8080", "/a", "", 301, "http://example.com:8080/a"},
		{"TLS", http.MethodGet, "www.example.com", "https://www.example.com/a", "", 301, "https://example.com/a"},
		{"a forwarded scheme", http.MethodGet, "www.example.com", "/a", "https", 301, "https://example.com/a"},
		{"an escaped path", http.MethodGet, "www.example.com", "/a%2Fb/c%20d", "", 301, "http://example.com/a%2Fb/c%20d"},
		{"a tenant", http.MethodPost, "acme.old.test", "/orders", "", 308, "http://acme.new.test/orders"},
		{"a port of the target", http.MethodGet, "legacy.test:8080", "/x", "", 302, "http://example.com:8443/x"},
		{"OPTIONS *", http.MethodOptions, "www.example.com", "*", "", 301, "http://example.com/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, nil)
			req.Host = tc.host
			if tc.proto != "" {
				req.Header.Set(HeaderXForwardedProto, tc.proto)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.code || rec.Header().Get(HeaderLocation) != tc.want {
				t.Errorf("%s %s%s = %d to %q, want %d to %q",
					tc.method, tc.host, tc.target, rec.Code, rec.Header().Get(HeaderLocation), tc.code, tc.want)
			}
		})
	}
}

func TestRedirectHostOwnsItsHost(t *testing.T) {
	t.Run("a route registered before", func(t *testing.T) {
		defer wantPanic(t, "already holds routes")
		r := newTestRouter()
		r.Host("www.example.com", func(h *Router[*tctx]) { h.GET("/", echoHost) })
		r.RedirectHost("www.example.com", "example.com", http.StatusMovedPermanently)
	})
	t.Run("a route registered after", func(t *testing.T) {
		defer wantPanic(t, "belongs to RedirectHost")
		r := newTestRouter()
		r.RedirectHost("www.example.com", "example.com", http.StatusMovedPermanently)
		r.Host("www.example.com", func(h *Router[*tctx]) { h.GET("/", echoHost) })
	})
	t.Run("a route of a Hosts scope that names it", func(t *testing.T) {
		defer wantPanic(t, "belongs to RedirectHost")
		r := newTestRouter()
		r.RedirectHost("www.example.com", "example.com", http.StatusMovedPermanently)
		r.Hosts([]string{"example.com", "www.example.com"}, func(h *Router[*tctx]) { h.GET("/", echoHost) })
	})
	t.Run("a second redirect", func(t *testing.T) {
		defer wantPanic(t, "already has a RedirectHost")
		r := newTestRouter()
		r.RedirectHost("www.example.com", "example.com", http.StatusMovedPermanently)
		r.RedirectHost("www.example.com", "example.org", http.StatusMovedPermanently)
	})
	t.Run("the target host", func(t *testing.T) {
		r := newTestRouter()
		r.RedirectHost("www.example.com", "example.com", http.StatusMovedPermanently)
		r.Host("example.com", func(h *Router[*tctx]) { h.GET("/", echoHost) })
		if rec := doHost(r, http.MethodGet, "example.com", "/"); rec.Code != http.StatusOK {
			t.Errorf("GET example.com/ = %d, want 200", rec.Code)
		}
	})
}

func TestRedirectHostRefusesALoop(t *testing.T) {
	captureLogs(t)
	r := newTestRouter()
	r.RedirectHost("*", "example.com", http.StatusMovedPermanently)

	for _, host := range []string{"example.com", "EXAMPLE.com:8080"} {
		if code, loc := follow(r, http.MethodGet, host, "/a"); code != http.StatusNotFound {
			t.Errorf("GET %s/a = %d to %q, want 404", host, code, loc)
		}
	}
	if code, loc := follow(r, http.MethodGet, "other.test", "/a"); code != http.StatusMovedPermanently || loc != "http://example.com/a" {
		t.Errorf("GET other.test/a = %d to %q, want 301 to http://example.com/a", code, loc)
	}
}

func TestRedirectHostPanicsOnABadCall(t *testing.T) {
	tests := []struct {
		name string
		call func(r *Router[*tctx])
		want string
	}{
		{"a bad pattern", func(r *Router[*tctx]) { r.RedirectHost("{x", "example.com", 301) }, "unbalanced"},
		{"an empty target", func(r *Router[*tctx]) { r.RedirectHost("www.example.com", "", 301) }, "needs a target host"},
		{"a target with a path", func(r *Router[*tctx]) { r.RedirectHost("www.example.com", "example.com/a", 301) }, "a host alone"},
		{"a URL target", func(r *Router[*tctx]) { r.RedirectHost("www.example.com", "https://example.com", 301) }, "bad port"},
		{"a bad port", func(r *Router[*tctx]) { r.RedirectHost("www.example.com", "example.com:99999", 301) }, "bad port"},
		{"an empty port", func(r *Router[*tctx]) { r.RedirectHost("www.example.com", "example.com:", 301) }, "bad port"},
		{"a wildcard target", func(r *Router[*tctx]) { r.RedirectHost("www.example.com", "*.example.com", 301) }, "wildcard"},
		{"an unknown parameter", func(r *Router[*tctx]) { r.RedirectHost("www.example.com", "{tenant}.example.com", 301) }, `"tenant"`},
		{"a target equal to the pattern", func(r *Router[*tctx]) { r.RedirectHost("{t}.example.com", "{t}.EXAMPLE.com.", 301) }, "points at itself"},
		{"status 200", func(r *Router[*tctx]) { r.RedirectHost("www.example.com", "example.com", http.StatusOK) }, "redirect status"},
		{"inside a host scope", func(r *Router[*tctx]) {
			r.Host("example.com", func(h *Router[*tctx]) { h.RedirectHost("www.example.com", "example.com", 301) })
		}, "inside another host scope"},
		{"inside a prefix", func(r *Router[*tctx]) {
			r.Route("/x", func(g *Router[*tctx]) { g.RedirectHost("www.example.com", "example.com", 301) })
		}, "scope with a prefix"},
		{"after serving", func(r *Router[*tctx]) {
			do(r, http.MethodGet, "/")
			r.RedirectHost("www.example.com", "example.com", 301)
		}, "after the router started serving"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer wantPanic(t, tc.want)
			tc.call(newTestRouter())
		})
	}
}

func wantPanic(t *testing.T, want string) {
	t.Helper()
	if msg := fmt.Sprint(recover()); !strings.Contains(msg, want) {
		t.Errorf("panic = %q, want one that mentions %q", msg, want)
	}
}
