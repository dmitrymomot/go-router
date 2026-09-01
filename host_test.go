package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func doHost(h http.Handler, method, host, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func echoHost(c *tctx) error {
	parts := []string{c.RouteHost() + "|" + c.RoutePattern()}
	for _, n := range c.ParamNames() {
		parts = append(parts, n+"="+c.Param(n))
	}
	return c.String(http.StatusOK, strings.Join(parts, " "))
}

func siteRouter() *Router[*tctx] {
	r := newTestRouter()

	r.Host("example.com", func(h *Router[*tctx]) {
		h.GET("/", echoHost)
		h.Route("/blog", func(b *Router[*tctx]) {
			b.GET("/", echoHost)
			b.GET("/{slug}", echoHost)
		})
	})

	r.Host("api.example.com", func(h *Router[*tctx]) {
		h.GET("/v1/users/{id}", echoHost)
	})

	r.Hosts([]string{"{tenant}.example.com", "*"}, func(h *Router[*tctx]) {
		h.GET("/", echoHost)
		h.GET("/settings", echoHost)
	})

	r.GET("/healthz", echoHost)
	r.GET("/{page}", echoHost)
	return r
}

func TestHostMatch(t *testing.T) {
	r := siteRouter()

	tests := []struct {
		host, path, want string
	}{
		{"example.com", "/", "example.com|/"},
		{"example.com", "/blog", "example.com|/blog"},
		{"example.com", "/blog/hello", "example.com|/blog/{slug} slug=hello"},
		{"api.example.com", "/v1/users/7", "api.example.com|/v1/users/{id} id=7"},
		{"acme.example.com", "/", "{tenant}.example.com|/ tenant=acme"},
		{"acme.example.com", "/settings", "{tenant}.example.com|/settings tenant=acme"},
		{"acme.com", "/", "*|/"},
		{"acme.com", "/settings", "*|/settings"},

		{"example.com", "/healthz", "|/healthz"},
		{"api.example.com", "/healthz", "|/healthz"},
		{"acme.com", "/healthz", "|/healthz"},

		{"api.example.com", "/legal", "|/{page} page=legal"},

		{"Example.COM:8443", "/", "example.com|/"},
		{"example.com.", "/", "example.com|/"},
		{"ACME.example.com", "/", "{tenant}.example.com|/ tenant=acme"},
	}
	for _, tt := range tests {
		t.Run(tt.host+tt.path, func(t *testing.T) {
			rec := doHost(r, http.MethodGet, tt.host, tt.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
			}
			if got := rec.Body.String(); got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHostMiss(t *testing.T) {
	r := newTestRouter()
	r.Host("example.com", func(h *Router[*tctx]) { h.GET("/", echoHost) })
	r.GET("/healthz", echoHost)

	if rec := doHost(r, http.MethodGet, "other.com", "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("healthz on an unknown host = %d, want 200", rec.Code)
	}
	if rec := doHost(r, http.MethodGet, "other.com", "/"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown host = %d, want 404", rec.Code)
	}
	if rec := doHost(r, http.MethodGet, "api.example.com", "/"); rec.Code != http.StatusNotFound {
		t.Errorf("path of another host = %d, want 404", rec.Code)
	}
}

func TestHostOnlyRouter(t *testing.T) {
	r := newTestRouter()
	r.Host("example.com", func(h *Router[*tctx]) { h.GET("/", echoHost) })

	if rec := doHost(r, http.MethodGet, "example.com", "/"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if rec := doHost(r, http.MethodGet, "other.com", "/"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHostPatternPrecedence(t *testing.T) {
	r := newTestRouter()
	for _, p := range []string{
		"www.example.com",
		"{tenant}.example.com",
		"{sub...}.example.com",
		"{a}.{b}.org",
		"*.test",
		"*",
	} {
		r.Host(p, func(h *Router[*tctx]) { h.GET("/", echoHost) })
	}

	tests := []struct{ host, want string }{
		{"www.example.com", "www.example.com|/"},
		{"acme.example.com", "{tenant}.example.com|/ tenant=acme"},
		{"eu.acme.example.com", "{sub...}.example.com|/ sub=eu.acme"},
		{"a.b.org", "{a}.{b}.org|/ a=a b=b"},
		{"x.test", "*.test|/"},
		{"anything.else", "*|/"},
		{"example.com", "*|/"},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			rec := doHost(r, http.MethodGet, tt.host, "/")
			if got := rec.Body.String(); got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHostLabelSyntax(t *testing.T) {
	r := newTestRouter()
	r.Host("{tenant:[a-z]+}.example.com", func(h *Router[*tctx]) { h.GET("/", echoHost) })
	r.Host("acme-{env}.example.net", func(h *Router[*tctx]) { h.GET("/", echoHost) })
	r.Host("{id:[0-9]+}.ip.example.org", func(h *Router[*tctx]) { h.GET("/", echoHost) })

	tests := []struct{ host, want string }{
		{"acme.example.com", "{tenant:[a-z]+}.example.com|/ tenant=acme"},
		{"acme-stage.example.net", "acme-{env}.example.net|/ env=stage"},
		{"42.ip.example.org", "{id:[0-9]+}.ip.example.org|/ id=42"},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := doHost(r, http.MethodGet, tt.host, "/").Body.String(); got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
		})
	}

	if rec := doHost(r, http.MethodGet, "acme1.example.com", "/"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHostSuffixIsALabelBoundary(t *testing.T) {
	r := newTestRouter()
	r.Host("{tenant}.example.com", func(h *Router[*tctx]) { h.GET("/", echoHost) })
	r.Host("{a}.{b}.example.net", func(h *Router[*tctx]) { h.GET("/", echoHost) })

	for _, host := range []string{"acmeexample.com", "example.com", "a.b.c.example.net", "b.example.net"} {
		t.Run(host, func(t *testing.T) {
			if rec := doHost(r, http.MethodGet, host, "/"); rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404; body %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestHostAndPathParams(t *testing.T) {
	r := newTestRouter()
	r.Host("{tenant}.example.com", func(h *Router[*tctx]) {
		h.GET("/users/{id}/posts/{postID}", echoHost)
		h.GET("/files/{path...}", echoHost)
	})

	rec := doHost(r, http.MethodGet, "acme.example.com", "/users/7/posts/9")
	want := "{tenant}.example.com|/users/{id}/posts/{postID} tenant=acme id=7 postID=9"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}

	rec = doHost(r, http.MethodGet, "acme.example.com", "/files/a/b.txt")
	want = "{tenant}.example.com|/files/{path...} tenant=acme path=a/b.txt"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestHostEscapedPathParams(t *testing.T) {
	r := newTestRouter()
	r.Host("{tenant}.example.com", func(h *Router[*tctx]) { h.GET("/tags/{name}", echoHost) })

	rec := doHost(r, http.MethodGet, "acme.example.com", "/tags/a%2Fb")
	want := "{tenant}.example.com|/tags/{name} tenant=acme name=a/b"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestHostMiddleware(t *testing.T) {
	var order []string
	mark := func(tag string) Middleware[*tctx] {
		return func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
			return func(c *tctx) error {
				order = append(order, tag)
				return next(c)
			}
		}
	}

	r := newTestRouter()
	r.Use(mark("root"))
	r.Host("example.com", func(h *Router[*tctx]) {
		h.Use(mark("site"))
		h.GET("/", echoHost)
	})
	r.Host("api.example.com", func(h *Router[*tctx]) {
		h.Use(mark("api"))
		h.GET("/", echoHost)
	})

	doHost(r, http.MethodGet, "example.com", "/")
	if strings.Join(order, ",") != "root,site" {
		t.Errorf("order = %v, want [root site]", order)
	}
	order = nil
	doHost(r, http.MethodGet, "api.example.com", "/")
	if strings.Join(order, ",") != "root,api" {
		t.Errorf("order = %v, want [root api]", order)
	}
}

func TestHostFallbacks(t *testing.T) {
	r := newTestRouter()
	r.NotFound(func(c *tctx) error { return c.String(http.StatusNotFound, "root 404") })
	r.ErrorHandler(func(c *tctx, err error) { _ = c.String(http.StatusTeapot, "root err") })

	r.Host("example.com", func(h *Router[*tctx]) {
		h.NotFound(func(c *tctx) error { return c.String(http.StatusNotFound, "site 404") })
		h.MethodNotAllowed(func(c *tctx) error { return c.String(http.StatusMethodNotAllowed, "site 405") })
		h.ErrorHandler(func(c *tctx, err error) { _ = c.String(http.StatusTeapot, "site err") })
		h.GET("/", echoHost)
		h.GET("/boom", func(c *tctx) error { return fmt.Errorf("boom") })
	})
	r.Host("api.example.com", func(h *Router[*tctx]) {
		h.GET("/boom", func(c *tctx) error { return fmt.Errorf("boom") })
	})

	tests := []struct {
		name, host, method, path string
		code                     int
		body                     string
	}{
		{"host 404", "example.com", http.MethodGet, "/nope", http.StatusNotFound, "site 404"},
		{"host 405", "example.com", http.MethodPost, "/", http.StatusMethodNotAllowed, "site 405"},
		{"host error", "example.com", http.MethodGet, "/boom", http.StatusTeapot, "site err"},

		{"root 404 on a host", "api.example.com", http.MethodGet, "/nope", http.StatusNotFound, "root 404"},
		{"root error on a host", "api.example.com", http.MethodGet, "/boom", http.StatusTeapot, "root err"},

		{"root 404", "other.com", http.MethodGet, "/nope", http.StatusNotFound, "root 404"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doHost(r, tt.method, tt.host, tt.path)
			if rec.Code != tt.code {
				t.Errorf("status = %d, want %d", rec.Code, tt.code)
			}
			if got := rec.Body.String(); got != tt.body {
				t.Errorf("body = %q, want %q", got, tt.body)
			}
		})
	}
}

func TestHostPanicUsesHostErrorHandler(t *testing.T) {
	r := newTestRouter()
	r.Host("example.com", func(h *Router[*tctx]) {
		h.ErrorHandler(func(c *tctx, err error) { _ = c.String(http.StatusTeapot, "site panic") })
		h.GET("/", func(c *tctx) error { panic("boom") })
	})

	rec := doHost(r, http.MethodGet, "example.com", "/")
	if rec.Code != http.StatusTeapot || rec.Body.String() != "site panic" {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body)
	}
}

func TestHostMethodNotAllowedAllow(t *testing.T) {
	r := newTestRouter()
	r.Host("example.com", func(h *Router[*tctx]) { h.GET("/x", echoHost) })
	r.POST("/x", echoHost)

	rec := doHost(r, http.MethodPut, "example.com", "/x")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get(HeaderAllow); got != "GET, HEAD, OPTIONS, POST" {
		t.Errorf("Allow = %q, want %q", got, "GET, HEAD, OPTIONS, POST")
	}
}

func TestHostAutoOptions(t *testing.T) {
	r := newTestRouter()
	r.Host("example.com", func(h *Router[*tctx]) { h.GET("/x", echoHost) })

	rec := doHost(r, http.MethodOptions, "example.com", "/x")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get(HeaderAllow); got != "GET, HEAD, OPTIONS" {
		t.Errorf("Allow = %q", got)
	}
}

func TestHostRedirectTrailingSlash(t *testing.T) {
	r := newTestRouter()
	r.RedirectTrailingSlash(true)
	r.Host("example.com", func(h *Router[*tctx]) { h.GET("/blog", echoHost) })

	rec := doHost(r, http.MethodGet, "example.com", "/blog/")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", rec.Code)
	}
	if got := rec.Header().Get(HeaderLocation); got != "/blog" {
		t.Errorf("Location = %q, want /blog", got)
	}
}

func TestHostHandlerAndRouter(t *testing.T) {
	r := newTestRouter()
	r.HostHandler("static.example.com", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprintf(w, "static %s", req.URL.Path)
	}))

	sub := New(func(http.ResponseWriter, *http.Request) *tctx { return &tctx{Tag: "sub"} })
	sub.GET("/v1/ping", func(c *tctx) error { return c.String(http.StatusOK, "sub "+c.RoutePattern()) })
	r.HostRouter("api.example.com", sub)

	if got := doHost(r, http.MethodGet, "static.example.com", "/css/app.css").Body.String(); got != "static /css/app.css" {
		t.Errorf("body = %q", got)
	}
	if got := doHost(r, http.MethodGet, "api.example.com", "/v1/ping").Body.String(); got != "sub /v1/ping" {
		t.Errorf("body = %q", got)
	}
}

func TestHostContextAccessors(t *testing.T) {
	r := newTestRouter()
	r.Host("{tenant}.example.com", func(h *Router[*tctx]) {
		h.GET("/", func(c *tctx) error {
			return c.String(http.StatusOK, c.Host()+" "+c.RouteHost()+" "+c.Param("tenant"))
		})
	})
	r.GET("/healthz", func(c *tctx) error {
		return c.String(http.StatusOK, c.Host()+" ["+c.RouteHost()+"]")
	})

	if got := doHost(r, http.MethodGet, "Acme.example.com:8080", "/").Body.String(); got != "acme.example.com {tenant}.example.com acme" {
		t.Errorf("body = %q", got)
	}
	if got := doHost(r, http.MethodGet, "other.com", "/healthz").Body.String(); got != "other.com []" {
		t.Errorf("body = %q", got)
	}
}

func TestHostRoutes(t *testing.T) {
	r := newTestRouter()
	r.GET("/healthz", echoHost)
	r.Host("api.example.com", func(h *Router[*tctx]) { h.GET("/v1/ping", echoHost) })
	r.Host("example.com", func(h *Router[*tctx]) { h.GET("/", echoHost) })

	want := []Route{
		{Host: "", Method: http.MethodGet, Pattern: "/healthz"},
		{Host: "api.example.com", Method: http.MethodGet, Pattern: "/v1/ping"},
		{Host: "example.com", Method: http.MethodGet, Pattern: "/"},
	}
	got := r.Routes()
	if len(got) != len(want) {
		t.Fatalf("got %d routes, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("route %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestHostSharedScopeMergesTrees(t *testing.T) {
	r := newTestRouter()
	r.Host("example.com", func(h *Router[*tctx]) { h.GET("/a", echoHost) })
	r.Host("example.com", func(h *Router[*tctx]) { h.GET("/b", echoHost) })

	for _, p := range []string{"/a", "/b"} {
		if rec := doHost(r, http.MethodGet, "example.com", p); rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", p, rec.Code)
		}
	}
}

func TestHostPanics(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
	}{
		{"port in a pattern", func() {
			newTestRouter().Host("example.com:8080", nil)
		}},
		{"empty pattern", func() {
			newTestRouter().Host("", nil)
		}},
		{"empty label", func() {
			newTestRouter().Host("a..com", nil)
		}},
		{"unicode literal", func() {
			newTestRouter().Host("bücher.example", nil)
		}},
		{"unbalanced braces", func() {
			newTestRouter().Host("{tenant.example.com", nil)
		}},
		{"partial wildcard", func() {
			newTestRouter().Host("x*.example.com", nil)
		}},
		{"catch-all is not first", func() {
			newTestRouter().Host("example.{sub...}.com", nil)
		}},
		{"duplicate host parameter", func() {
			newTestRouter().Host("{a}.{a}.com", nil)
		}},
		{"no pattern", func() {
			newTestRouter().Hosts(nil, nil)
		}},
		{"host inside host", func() {
			r := newTestRouter()
			r.Host("example.com", func(h *Router[*tctx]) {
				h.Host("api.example.com", func(g *Router[*tctx]) { g.GET("/", echoHost) })
			})
			r.Routes()
		}},
		{"host parameter clashes with a path parameter", func() {
			r := newTestRouter()
			r.Host("{tenant}.example.com", func(h *Router[*tctx]) { h.GET("/{tenant}", echoHost) })
			r.Routes()
		}},
		{"host after the router started", func() {
			r := newTestRouter()
			r.GET("/", echoHost)
			do(r, http.MethodGet, "/")
			r.Host("example.com", nil)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("want a panic")
				}
			}()
			tt.fn()
		})
	}
}

func TestNormalizeHost(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"example.com", "example.com"},
		{"Example.COM", "example.com"},
		{"example.com:8080", "example.com"},
		{"example.com.", "example.com"},
		{"EXAMPLE.com.:443", "example.com"},
		{"[::1]:8080", "::1"},
		{"[::1]", "::1"},
		{"[2001:DB8::1]:65535", "2001:db8::1"},
		{"[example.com]", ""},
		{"[example.com]attacker.invalid", ""},
		{"[::1", ""},
		{"[::1]junk", ""},
		{"[::1]:", ""},
		{"[::1]:65536", ""},
		{"::1", ""},
		{"example.com:not-a-port", ""},
		{"example.com:80:garbage", ""},
		{"example.com:", ""},
		{"example.com:65536", ""},
		{":80", ""},
		{".example.com", ""},
		{"example..com", ""},
		{"example.com..", ""},
		{"exa mple.com", ""},
		{"example.com/path", ""},
		{"example.com@evil.invalid", ""},
		{"bücher.example", ""},
	}
	for _, tt := range tests {
		if got := normalizeHost(tt.in); got != tt.want {
			t.Errorf("normalizeHost(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHostPatternPreservesNamesAndRegexSyntax(t *testing.T) {
	r := newTestRouter()
	r.Host(`{Tenant:\D+}.EXAMPLE.com`, func(h *Router[*tctx]) {
		h.GET("/", func(c *tctx) error {
			return c.String(http.StatusOK, c.RouteHost()+"|"+c.Param("Tenant")+"|"+c.Param("tenant"))
		})
	})

	if got := doHost(r, http.MethodGet, "Acme.Example.com", "/").Body.String(); got != `{Tenant:\D+}.example.com|acme|` {
		t.Errorf("alphabetic host = %q", got)
	}
	if rec := doHost(r, http.MethodGet, "123.example.com", "/"); rec.Code != http.StatusNotFound {
		t.Errorf("numeric host status = %d, want 404", rec.Code)
	}
}

func TestDynamicHostPrecedenceIsStructural(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			r := newTestRouter()
			register := []func(){
				func() {
					r.Host("*.example.com", func(h *Router[*tctx]) {
						h.GET("/", func(c *tctx) error { return c.String(http.StatusOK, "wildcard") })
					})
				},
				func() {
					r.Host("{z}.example.com", func(h *Router[*tctx]) {
						h.GET("/", func(c *tctx) error { return c.String(http.StatusOK, "parameter "+c.Param("z")) })
					})
				},
				func() {
					r.Host("{a:[0-9]+}.example.com", func(h *Router[*tctx]) {
						h.GET("/", func(c *tctx) error { return c.String(http.StatusOK, "regex "+c.Param("a")) })
					})
				},
			}
			if reverse {
				slices.Reverse(register)
			}
			for _, add := range register {
				add()
			}

			if got := doHost(r, http.MethodGet, "123.example.com", "/").Body.String(); got != "regex 123" {
				t.Errorf("numeric host chose %q", got)
			}
			if got := doHost(r, http.MethodGet, "acme.example.com", "/").Body.String(); got != "parameter acme" {
				t.Errorf("alphabetic host chose %q", got)
			}
		})
	}
}

func TestEquivalentDynamicHostPatternsAreRejected(t *testing.T) {
	r := newTestRouter()
	r.Host("{tenant}.example.com", func(h *Router[*tctx]) { h.GET("/a", echoHost) })
	mustPanicContaining(t, "same match shape", func() {
		r.Host("{account}.example.com", func(h *Router[*tctx]) { h.GET("/b", echoHost) })
	})
}

func TestMalformedAuthoritiesNeverMatchHostScopes(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error { return c.String(http.StatusOK, "root") })
	r.Host("example.com", func(h *Router[*tctx]) {
		h.GET("/", func(c *tctx) error { return c.String(http.StatusOK, "exact") })
	})
	r.Host("*", func(h *Router[*tctx]) {
		h.GET("/", func(c *tctx) error { return c.String(http.StatusOK, "wildcard") })
	})

	for _, host := range []string{"[example.com]", "[example.com]attacker.invalid", "example.com:not-a-port", "example.com:80:garbage"} {
		if got := doHost(r, http.MethodGet, host, "/").Body.String(); got != "root" {
			t.Errorf("Host %q reached %q, want host-free route", host, got)
		}
	}
	if got := doHost(r, http.MethodGet, "example.com:443", "/").Body.String(); got != "exact" {
		t.Errorf("valid authority reached %q, want exact host", got)
	}
}

func TestUnicodeHostsRequirePunycode(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error { return c.String(http.StatusOK, "root") })
	r.Host("xn--bcher-kva.example", func(h *Router[*tctx]) {
		h.GET("/", func(c *tctx) error { return c.String(http.StatusOK, "punycode") })
	})
	if got := doHost(r, http.MethodGet, "bücher.example", "/").Body.String(); got != "root" {
		t.Errorf("Unicode host reached %q, want root", got)
	}
	if got := doHost(r, http.MethodGet, "xn--bcher-kva.example", "/").Body.String(); got != "punycode" {
		t.Errorf("punycode host reached %q", got)
	}
}

func TestHostPooledContextClearsHost(t *testing.T) {
	r := NewPooled(func() *tctx { return new(tctx) }, func(c *tctx) { c.Tag = "" })
	r.Host("{tenant}.example.com", func(h *Router[*tctx]) {
		h.GET("/", func(c *tctx) error {
			return c.String(http.StatusOK, c.Host()+"|"+c.RouteHost()+"|"+c.Param("tenant"))
		})
	})
	r.GET("/healthz", func(c *tctx) error {
		return c.String(http.StatusOK, c.Host()+"|"+c.RouteHost()+"|"+c.Param("tenant"))
	})

	if got := doHost(r, http.MethodGet, "a.example.com", "/").Body.String(); got != "a.example.com|{tenant}.example.com|a" {
		t.Fatalf("body = %q", got)
	}
	if got := doHost(r, http.MethodGet, "other.com", "/healthz").Body.String(); got != "other.com||" {
		t.Fatalf("body = %q", got)
	}
}

func TestHostNilPanics(t *testing.T) {
	for name, fn := range map[string]func(){
		"HostRouter":  func() { newTestRouter().HostRouter("example.com", (*Router[*tctx])(nil)) },
		"HostHandler": func() { newTestRouter().HostHandler("example.com", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("want a panic")
				}
			}()
			fn()
		})
	}
}

func TestHostRestLabel(t *testing.T) {
	r := newTestRouter()
	r.Host("{sub...}.example.com", func(h *Router[*tctx]) { h.GET("/", echoHost) })

	tests := []struct{ host, want string }{
		{"a.example.com", "{sub...}.example.com|/ sub=a"},
		{"a.b.c.example.com", "{sub...}.example.com|/ sub=a.b.c"},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := doHost(r, http.MethodGet, tt.host, "/").Body.String(); got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
		})
	}
	if rec := doHost(r, http.MethodGet, "example.com", "/"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHostFallbackAppliesToEveryPatternOfAScope(t *testing.T) {
	r := newTestRouter()
	r.Hosts([]string{"{tenant}.example.com", "*"}, func(h *Router[*tctx]) {
		h.NotFound(func(c *tctx) error { return c.String(http.StatusNotFound, "tenant 404") })
		h.GET("/", echoHost)
	})

	for _, host := range []string{"acme.example.com", "acme.com"} {
		t.Run(host, func(t *testing.T) {
			rec := doHost(r, http.MethodGet, host, "/nope")
			if got := rec.Body.String(); got != "tenant 404" {
				t.Errorf("body = %q, want %q", got, "tenant 404")
			}
		})
	}
}

func TestHostRedirectTrailingSlashFallsBackToHostFreeRoutes(t *testing.T) {
	r := newTestRouter()
	r.RedirectTrailingSlash(true)
	r.Host("example.com", func(h *Router[*tctx]) { h.GET("/site", echoHost) })
	r.GET("/healthz", echoHost)

	rec := doHost(r, http.MethodGet, "example.com", "/healthz/")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", rec.Code)
	}
	if got := rec.Header().Get(HeaderLocation); got != "/healthz" {
		t.Errorf("Location = %q, want /healthz", got)
	}
}

func TestPerHostErrorHandlers(t *testing.T) {
	r := newTestRouter()
	r.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), "root: "+err.Error()) })

	r.Host("api.example.com", func(h *Router[*tctx]) {
		h.ErrorHandler(func(c *tctx, err error) {
			_ = c.JSON(StatusOf(err), map[string]string{"error": err.Error()})
		})
		h.GET("/boom", func(c *tctx) error { return ErrForbidden })
	})

	r.Host("example.com", func(h *Router[*tctx]) {
		h.ErrorHandler(func(c *tctx, err error) {
			_ = c.HTML(StatusOf(err), "<h1>example.com</h1>")
		})
		h.GET("/boom", func(c *tctx) error { return ErrForbidden })
	})

	r.Host("{tenant}.example.com", func(h *Router[*tctx]) {
		h.ErrorHandler(func(c *tctx, err error) {
			_ = c.HTML(StatusOf(err), fmt.Sprintf("<h1>%s</h1>", c.Param("tenant")))
		})
		h.GET("/boom", func(c *tctx) error { return ErrForbidden })
	})

	tests := []struct{ host, path, want string }{
		{"api.example.com", "/boom", `{"error":"403 Forbidden"}`},
		{"example.com", "/boom", "<h1>example.com</h1>"},
		{"acme.example.com", "/boom", "<h1>acme</h1>"},
		{"other.com", "/boom", "root: 404 Not Found"},

		{"api.example.com", "/nope", `{"error":"404 Not Found"}`},
		{"acme.example.com", "/nope", "<h1>acme</h1>"},
	}
	for _, tt := range tests {
		t.Run(tt.host+tt.path, func(t *testing.T) {
			got := doHost(r, http.MethodGet, tt.host, tt.path).Body.String()
			if got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHostParamsSurviveTheHostFreeWalk(t *testing.T) {
	build := func() *Router[*tctx] {
		r := newTestRouter()
		r.Host("{tenant}.example.com", func(h *Router[*tctx]) {
			h.NotFound(func(c *tctx) error {
				return c.String(http.StatusNotFound, "404 tenant="+c.Param("tenant"))
			})
			h.GET("/", echoHost)
		})
		r.GET("/{page}", echoHost)
		return r
	}

	r := build()
	if got := doHost(r, http.MethodGet, "acme.example.com", "/nope/deep").Body.String(); got != "404 tenant=acme" {
		t.Errorf("body = %q, want %q", got, "404 tenant=acme")
	}

	rs := build()
	rs.RedirectTrailingSlash(true)
	if got := doHost(rs, http.MethodGet, "acme.example.com", "/nope/deep/").Body.String(); got != "404 tenant=acme" {
		t.Errorf("body after the slash check = %q, want %q", got, "404 tenant=acme")
	}
}

func TestHostInheritsTheFallbackOfTheRoot(t *testing.T) {
	r := newTestRouter()
	r.Group(func(g *Router[*tctx]) {
		g.NotFound(func(c *tctx) error { return c.String(http.StatusNotFound, "custom 404") })
		g.MethodNotAllowed(func(c *tctx) error { return c.String(http.StatusMethodNotAllowed, "custom 405") })
	})
	r.Host("example.com", func(h *Router[*tctx]) { h.GET("/", echoHost) })

	if got := doHost(r, http.MethodGet, "other.invalid", "/nope").Body.String(); got != "custom 404" {
		t.Errorf("404 with no host = %q, want %q", got, "custom 404")
	}
	if got := doHost(r, http.MethodGet, "example.com", "/nope").Body.String(); got != "custom 404" {
		t.Errorf("404 on a host = %q, want %q", got, "custom 404")
	}
	if got := doHost(r, http.MethodPost, "example.com", "/").Body.String(); got != "custom 405" {
		t.Errorf("405 on a host = %q, want %q", got, "custom 405")
	}
}

func TestHostFreeRouteUsesTheRootFallbacks(t *testing.T) {
	r := newTestRouter()
	r.MethodNotAllowed(func(c *tctx) error { return c.String(http.StatusMethodNotAllowed, "root 405") })
	r.ErrorHandler(func(c *tctx, err error) { _ = c.String(http.StatusTeapot, "root err") })
	r.Host("example.com", func(h *Router[*tctx]) {
		h.MethodNotAllowed(func(c *tctx) error { return c.String(http.StatusMethodNotAllowed, "host 405") })
		h.ErrorHandler(func(c *tctx, err error) { _ = c.String(http.StatusTeapot, "host err") })
		h.GET("/site", echoHost)
	})
	r.GET("/healthz", echoHost)
	r.GET("/boom", func(c *tctx) error { return ErrForbidden })

	tests := []struct {
		name, method, path, want string
	}{
		{"405 on a host-free route", http.MethodPost, "/healthz", "root 405"},
		{"error on a host-free route", http.MethodGet, "/boom", "root err"},
		{"405 on a route of the host", http.MethodPost, "/site", "host 405"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := doHost(r, tt.method, "example.com", tt.path).Body.String(); got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
		})
	}

	r2 := newTestRouter()
	r2.Host("example.com", func(h *Router[*tctx]) { h.GET("/site", echoHost) })
	r2.GET("/healthz", echoHost)
	if got := doHost(r2, http.MethodGet, "example.com", "/healthz").Body.String(); got != "|/healthz" {
		t.Errorf("body = %q, want %q", got, "|/healthz")
	}
}

func TestHostNestedScopePanicsAtRegistration(t *testing.T) {
	for _, tt := range []struct {
		name string
		fn   func(h *Router[*tctx])
	}{
		{"directly", func(h *Router[*tctx]) { h.Host("api.example.com", nil) }},
		{"through a group", func(h *Router[*tctx]) {
			h.Group(func(g *Router[*tctx]) { g.Host("api.example.com", nil) })
		}},
		{"through a route", func(h *Router[*tctx]) {
			h.Route("/v1", func(g *Router[*tctx]) { g.Host("api.example.com", nil) })
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("want a panic at registration, not at the first request")
				}
			}()
			newTestRouter().Host("example.com", tt.fn)
		})
	}
}

func TestPerHostErrorHandlersIgnoreSetterOrder(t *testing.T) {
	build := func(late bool) *Router[*tctx] {
		r := newTestRouter()
		if !late {
			r.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), "root") })
		}
		r.Host("api.example.com", func(h *Router[*tctx]) {
			h.GET("/boom", func(*tctx) error { return ErrForbidden })
			h.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), "api") })
		})
		r.Host("{tenant}.example.com", func(h *Router[*tctx]) {
			h.GET("/boom", func(*tctx) error { return ErrForbidden })
			h.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), c.Param("tenant")) })
		})
		if late {
			r.ErrorHandler(func(c *tctx, err error) { _ = c.String(StatusOf(err), "root") })
		}
		return r
	}

	for _, late := range []bool{false, true} {
		name := "root handler first"
		if late {
			name = "root handler last"
		}
		t.Run(name, func(t *testing.T) {
			r := build(late)
			for _, tc := range []struct{ host, path, want string }{
				{"api.example.com", "/boom", "api"},
				{"acme.example.com", "/boom", "acme"},
				{"other.com", "/boom", "root"},
				{"api.example.com", "/nope", "api"},
			} {
				if got := doHost(r, http.MethodGet, tc.host, tc.path).Body.String(); got != tc.want {
					t.Errorf("%s%s = %q, want %q", tc.host, tc.path, got, tc.want)
				}
			}
		})
	}
}
