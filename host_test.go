package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// doHost sends a request that names a host.
func doHost(h http.Handler, method, host, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// echoHost writes the host pattern, the path pattern and every parameter.
func echoHost(c *tctx) error {
	parts := []string{c.RouteHost() + "|" + c.RoutePattern()}
	for _, n := range c.ParamNames() {
		parts = append(parts, n+"="+c.Param(n))
	}
	return c.String(http.StatusOK, strings.Join(parts, " "))
}

// siteRouter builds the four cases that a multi-tenant service needs: a main
// site, a fixed subdomain, a subdomain per tenant, and a domain of the tenant's
// own.
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

	r.GET("/healthz", echoHost) // every host answers it
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

		// A route outside every host scope answers on any host.
		{"example.com", "/healthz", "|/healthz"},
		{"api.example.com", "/healthz", "|/healthz"},
		{"acme.com", "/healthz", "|/healthz"},

		// The port, the case and a trailing dot do not take part.
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

	// A host that no pattern claims falls back to the host-free routes, and
	// then to 404.
	if rec := doHost(r, http.MethodGet, "other.com", "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("healthz on an unknown host = %d, want 200", rec.Code)
	}
	if rec := doHost(r, http.MethodGet, "other.com", "/"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown host = %d, want 404", rec.Code)
	}
	// The path exists on another host only.
	if rec := doHost(r, http.MethodGet, "api.example.com", "/"); rec.Code != http.StatusNotFound {
		t.Errorf("path of another host = %d, want 404", rec.Code)
	}
}

func TestHostOnlyRouter(t *testing.T) {
	// A router whose every route sits inside a host scope answers nothing else.
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

	// A label that the regular expression rejects finds no route.
	if rec := doHost(r, http.MethodGet, "acme1.example.com", "/"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHostSuffixIsALabelBoundary(t *testing.T) {
	r := newTestRouter()
	r.Host("{tenant}.example.com", func(h *Router[*tctx]) { h.GET("/", echoHost) })
	r.Host("{a}.{b}.example.net", func(h *Router[*tctx]) { h.GET("/", echoHost) })

	// The static tail of a pattern starts at a dot, so a host that merely ends
	// in the same letters does not match.
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

		// api.example.com sets none of them, so the root ones apply.
		{"root 404 on a host", "api.example.com", http.MethodGet, "/nope", http.StatusNotFound, "root 404"},
		{"root error on a host", "api.example.com", http.MethodGet, "/boom", http.StatusTeapot, "root err"},

		// No host matched at all.
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
	r.POST("/x", echoHost) // every host

	rec := doHost(r, http.MethodPut, "example.com", "/x")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	// The Allow header names the methods of both trees.
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
	// A route outside a host scope still reads the request host.
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
	// Two Host calls that name the same pattern share one tree.
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
	}
	for _, tt := range tests {
		if got := normalizeHost(tt.in); got != tt.want {
			t.Errorf("normalizeHost(%q) = %q, want %q", tt.in, got, tt.want)
		}
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
	// The next request reuses the context, so nothing of the first may remain.
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
	// A catch-all label needs at least one label of its own.
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
	// The root renders anything that no host claims.
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

		// A 404 on a host must reach that host's renderer too.
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
