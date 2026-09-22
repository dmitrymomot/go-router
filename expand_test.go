package router

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestExpandFillsEveryPatternShape(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		pairs   []string
		want    string
	}{
		{"a static route", "/", nil, "/"},
		{"one parameter", "/users/{id}", []string{"id", "7"}, "/users/7"},
		{"a regular expression parameter", "/orders/{id:[0-9]+}", []string{"id", "42"}, "/orders/42"},
		{"a class parameter", "/orders/{id:int}", []string{"id", "42"}, "/orders/42"},
		{"two parameters", "/blog/{year}/{slug}", []string{"year", "2026", "slug", "hello"}, "/blog/2026/hello"},
		{"a partial segment", "/reports/rep-{date}.csv", []string{"date", "20260102"}, "/reports/rep-20260102.csv"},
		{"two parameters in one segment", "/files/{name}.{ext}", []string{"name", "notes", "ext", "txt"}, "/files/notes.txt"},
		{"a catch-all", "/tree/{path...}", []string{"path", "a/b/c.txt"}, "/tree/a/b/c.txt"},
		{"an empty catch-all", "/tree/{path...}", []string{"path", ""}, "/tree"},
		{"an empty catch-all at the root", "/{path...}", []string{"path", ""}, "/"},
		{"an anonymous catch-all", "/assets/*", []string{"*", "css/app.css"}, "/assets/css/app.css"},
		{"a value that needs escaping", "/users/{id}", []string{"id", "a b/c"}, "/users/a%20b%2Fc"},
		{"a catch-all keeps its separators", "/tree/{path...}", []string{"path", "a b/c d"}, "/tree/a%20b/c%20d"},
		{"a trailing slash", "/users/{id}/", []string{"id", "7"}, "/users/7"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Expand(tc.pattern, tc.pairs...)
			if err != nil {
				t.Fatalf("Expand(%q) = %v", tc.pattern, err)
			}
			if got != tc.want {
				t.Errorf("Expand(%q) = %q, want %q", tc.pattern, got, tc.want)
			}
		})
	}
}

func TestExpandBuildsAPathThatRoutesBack(t *testing.T) {
	patterns := []string{
		"/", "/users/{id}", "/blog/{year}/{slug}", "/reports/rep-{date}.csv", "/tree/{path...}",
		"/r/{env}-{name}", "/s/{name}.{ext}", "/f/{p...}", "/n/{id:int}",
	}
	r := newTestRouter()
	for _, p := range patterns {
		r.GET(p, echoRoute)
	}

	tests := []struct {
		pattern string
		pairs   []string
		want    string
	}{
		{"/", nil, "/"},
		{"/users/{id}", []string{"id", "7"}, "/users/{id} id=7"},
		{"/users/{id}", []string{"id", "a b"}, "/users/{id} id=a b"},
		{"/users/{id}", []string{"id", "a/b?c#d%e"}, "/users/{id} id=a/b?c#d%e"},
		{"/blog/{year}/{slug}", []string{"year", "2026", "slug", "hello"}, "/blog/{year}/{slug} year=2026 slug=hello"},
		{"/reports/rep-{date}.csv", []string{"date", "20260102"}, "/reports/rep-{date}.csv date=20260102"},
		{"/tree/{path...}", []string{"path", "a/b.txt"}, "/tree/{path...} path=a/b.txt"},
		{"/tree/{path...}", []string{"path", ""}, "/tree/{path...} path="},
		{"/r/{env}-{name}", []string{"env", "prod", "name", "web"}, "/r/{env}-{name} env=prod name=web"},
		{"/r/{env}-{name}", []string{"env", "pre-prod", "name", "web"}, "/r/{env}-{name} env=pre-prod name=web"},
		{"/s/{name}.{ext}", []string{"name", "a.b", "ext", "txt"}, "/s/{name}.{ext} name=a.b ext=txt"},
		{"/f/{p...}", []string{"p", "/x"}, "/f/{p...} p=/x"},
		{"/n/{id:int}", []string{"id", "42"}, "/n/{id:int} id=42"},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.pattern, tc.pairs), func(t *testing.T) {
			path := MustExpand(tc.pattern, tc.pairs...)
			rec := do(r, http.MethodGet, path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %q: status = %d, want 200", path, rec.Code)
			}
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("GET %q reached %q, want %q", path, got, tc.want)
			}
		})
	}
}

func TestExpandEscapesTheQuery(t *testing.T) {
	const token = "a&b=c+d#e f/g?h"
	got, err := Expand("/login/link?token={token}", "token", token)
	if err != nil {
		t.Fatal(err)
	}
	path, query, _ := strings.Cut(got, "?")
	if path != "/login/link" {
		t.Errorf("path = %q, want /login/link", path)
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("token") != token {
		t.Errorf("token reads back as %q, want %q", q.Get("token"), token)
	}

	tests := []struct {
		name    string
		pattern string
		pairs   []string
		want    string
	}{
		{"an empty value", "/login/link?token={token}", []string{"token", ""}, "/login/link?token="},
		{"a name in the path and the query", "/u/{id}?back={id}", []string{"id", "a b"}, "/u/a%20b?back=a+b"},
		{"an empty catch-all trims the path, not the query", "/tree/{p...}?x={x}", []string{"p", "", "x", "1"}, "/tree?x=1"},
		{"literal pairs", "/search?sort=asc&q={q}", []string{"q", "go"}, "/search?sort=asc&q=go"},
		{"literal text after the value", "/search?q={q}&sort=asc", []string{"q", "go"}, "/search?q=go&sort=asc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Expand(tc.pattern, tc.pairs...)
			if err != nil {
				t.Fatalf("Expand(%q) = %v", tc.pattern, err)
			}
			if got != tc.want {
				t.Errorf("Expand(%q) = %q, want %q", tc.pattern, got, tc.want)
			}
		})
	}
}

func TestExpandRefusesAnEmptyValueWhereTheRouterWould(t *testing.T) {
	tests := []struct {
		pattern string
		pairs   []string
		ok      bool
	}{
		{"/users/{id}", []string{"id", ""}, false},
		{"/r/{env}-{name}", []string{"env", "", "name", "web"}, false},
		{"/tree/{p...}", []string{"p", ""}, true},
		{"/a?q={q}", []string{"q", ""}, true},
		{"{tenant}.example.com", []string{"tenant", ""}, false},
	}
	for _, tc := range tests {
		t.Run(tc.pattern, func(t *testing.T) {
			got, err := Expand(tc.pattern, tc.pairs...)
			if (err == nil) != tc.ok {
				t.Errorf("Expand(%q) = %q, %v; want ok = %v", tc.pattern, got, err, tc.ok)
			}
		})
	}
}

// The router binds "/old//evil.com" into a catch-all as "/evil.com". A path
// built from it that starts with "//" is a link to another host.
func TestExpandRefusesANetworkPathReference(t *testing.T) {
	for _, v := range []string{"/evil.com", "//evil.com", "/"} {
		got, err := Expand("/{p...}", "p", v)
		if err == nil {
			t.Errorf("Expand(/{p...}, %q) = %q, want an error", v, got)
		}
		if strings.HasPrefix(got, "//") {
			t.Errorf("Expand(/{p...}, %q) = %q, which starts with //", v, got)
		}
	}
	if got, err := Expand("/files/{p...}", "p", "/a"); err != nil || got != "/files//a" {
		t.Errorf("Expand(/files/{p...}, /a) = %q, %v; want /files//a", got, err)
	}
	if got, err := Expand("/{p...}", "p", `\evil.com`); err != nil || got != "/%5Cevil.com" {
		t.Errorf(`Expand(/{p...}, \evil.com) = %q, %v; want /%%5Cevil.com`, got, err)
	}
}

// A browser resolves "/users/../delete" to "/delete", so such a path does not
// reach the route it was built from.
func TestExpandRefusesADotSegment(t *testing.T) {
	for _, tc := range []struct{ pattern, name, value string }{
		{"/users/{id}/delete", "id", ".."},
		{"/users/{id}", "id", "."},
		{"/files/{p...}", "p", "a/../../admin"},
		{"/files/{p...}", "p", "./a"},
		{"/r/{id:[a-z.]+}", "id", ".."},
		{"/r/{n}.", "n", "."},
	} {
		if got, err := Expand(tc.pattern, tc.name, tc.value); err == nil {
			t.Errorf("Expand(%s, %q) = %q, want an error", tc.pattern, tc.value, got)
		}
	}
	for _, tc := range []struct{ pattern, name, value, want string }{
		{"/users/{id}", "id", "...", "/users/..."},
		{"/users/{id}", "id", ".env", "/users/.env"},
		{"/files/{p...}", "p", "a/..b", "/files/a/..b"},
	} {
		if got, err := Expand(tc.pattern, tc.name, tc.value); err != nil || got != tc.want {
			t.Errorf("Expand(%s, %q) = %q, %v; want %q", tc.pattern, tc.value, got, err, tc.want)
		}
	}
}

func TestExpandChecksBuiltinClassesAndAdmitsDeclaredOnes(t *testing.T) {
	tests := []struct {
		pattern, name, value string
		ok                   bool
	}{
		{"/n/{id:int}", "id", "abc", false},
		{"/n/{id:int}", "id", "42", true},
		{"/u/{id:uuid}", "id", "not-a-uuid", false},
		{"/u/{id:uuid}", "id", "0198c5b6-3f0e-7b3a-9c1d-2f4e6a8b0c1d", true},
		{"/t/{s:slug}", "s", "Bad Slug", false},
		{"/c/{c:shortid}", "c", "anything at all", true},
		{"/c/x-{c:shortid}", "c", "anything", true},
	}
	for _, tc := range tests {
		t.Run(tc.pattern+" "+tc.value, func(t *testing.T) {
			got, err := Expand(tc.pattern, tc.name, tc.value)
			if (err == nil) != tc.ok {
				t.Errorf("Expand(%q, %q) = %q, %v; want ok = %v", tc.pattern, tc.value, got, err, tc.ok)
			}
		})
	}

	r := newTestRouter()
	r.ParamClass("shortid", func(s string) bool { return len(s) == 6 })
	r.GET("/c/{c:shortid}", echoRoute)
	if rec := do(r, http.MethodGet, MustExpand("/c/{c:shortid}", "c", "abc123")); rec.Body.String() != "/c/{c:shortid} c=abc123" {
		t.Errorf("the expanded path reached %d %q", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, MustExpand("/c/{c:shortid}", "c", "toolongforit")); rec.Code != http.StatusNotFound {
		t.Errorf("a value the declared class refuses answered %d, want 404", rec.Code)
	}
}

func TestExpandReportsABadCall(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		pairs   []string
		want    string
	}{
		{"a missing parameter", "/users/{id}", nil, `needs a value for "id"`},
		{"two missing, sorted", "/blog/{year}/{slug}", nil, `needs a value for "slug", "year"`},
		{"a spare parameter", "/users/{id}", []string{"id", "7", "page", "2"}, `has no parameter "page"`},
		{"only spare parameters, sorted", "/", []string{"b", "1", "a", "2"}, `has no parameter "a", "b"`},
		{"a parameter given twice", "/users/{id}", []string{"id", "7", "id", "8"}, `got the parameter "id" twice`},
		{"an odd argument count", "/users/{id}", []string{"id"}, "alternating names and values"},
		{"a malformed pattern", "/x/{", nil, ValidatePattern("/x/{").Error()},
		{"a class that cannot exist", "/x/{id:9x}", []string{"id", "1"}, ValidatePattern("/x/{id:9x}").Error()},
		{"a path without its slash", "users/{id}", []string{"id", "7"}, `must start with "/"`},
		{"a query without its path", "?q={q}", []string{"q", "go"}, `must start with "/"`},
		{"a query placeholder with a constraint", "/a?q={q:int}", []string{"q", "1"}, "bare {name}"},
		{"a query catch-all", "/a?q={q...}", []string{"q", "1"}, "bare {name}"},
		{"a fragment in the query", "/a?q={q}#top", []string{"q", "1"}, `"#"`},
		{"a space in the query", "/a?q={q} x", []string{"q", "1"}, `" "`},
		{"an unbalanced query", "/a?q={q", []string{"q", "1"}, "unbalanced"},
		{"a stray brace in the query", "/a?q=}", nil, "unbalanced"},
		{"an empty plain value", "/users/{id}", []string{"id", ""}, "is empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Expand(tc.pattern, tc.pairs...)
			if err == nil {
				t.Fatalf("Expand(%q) = %q, want an error that mentions %q", tc.pattern, got, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Expand(%q) = %v, want an error that mentions %q", tc.pattern, err, tc.want)
			}
		})
	}
}

func TestExpandRefusesAValueThatReadsBackAsAnotherOne(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		pairs   []string
	}{
		{"a value that carries the literal of its segment", "/r/{env}-{name}", []string{"env", "prod", "name", "web-api"}},
		{"a catch-all that is nothing but a separator", "/f/{p...}", []string{"p", "/"}},
		{"a catch-all with a trailing separator", "/f/{p...}", []string{"p", "x/"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Expand(tc.pattern, tc.pairs...); err == nil {
				t.Fatalf("Expand(%q) = %q, want an error; the path reads back as other values", tc.pattern, got)
			}
		})
	}
}

func TestExpandRefusesAValueThePatternRejects(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		pairs   []string
	}{
		{"a whole segment", "/orders/{id:[0-9]+}", []string{"id", "abc"}},
		{"a template segment", "/reports/rep-{date:[0-9]{8}}.csv", []string{"date", "nope"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Expand(tc.pattern, tc.pairs...); err == nil {
				t.Fatalf("Expand(%q) = %q, want an error; the pattern rejects the value", tc.pattern, got)
			}
		})
	}
}

func TestExpandChecksRegexesAgainstDecodedValues(t *testing.T) {
	if got, err := Expand("/files/{name:[a-z]+}", "name", "a/b"); err == nil {
		t.Fatalf("Expand = %q, want a regex mismatch", got)
	}
	got, err := Expand("/encoded/{value:.*}", "value", "%2F")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/encoded/%252F" {
		t.Errorf("Expand = %q, want exactly-once encoding", got)
	}
}

func TestExpandFillsAHostPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		pairs   []string
		want    string
	}{
		{"an exact host", "example.com", nil, "example.com"},
		{"a leading label", "{tenant}.example.com", []string{"tenant", "acme"}, "acme.example.com"},
		{"a class", "{tenant:slug}.example.com", []string{"tenant", "acme-2"}, "acme-2.example.com"},
		{"a template label", "app-{env}.example.com", []string{"env", "prod"}, "app-prod.example.com"},
		{"two labels", "{svc}.{region}.example.com", []string{"svc", "api", "region", "eu"}, "api.eu.example.com"},
		{"a rest label", "{sub...}.example.com", []string{"sub", "a.b"}, "a.b.example.com"},
		{"upper-case literals", "{tenant}.Example.COM", []string{"tenant", "acme"}, "acme.example.com"},
		{"a trailing dot", "example.com.", nil, "example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Expand(tc.pattern, tc.pairs...)
			if err != nil {
				t.Fatalf("Expand(%q) = %v", tc.pattern, err)
			}
			if got != tc.want {
				t.Errorf("Expand(%q) = %q, want %q", tc.pattern, got, tc.want)
			}
		})
	}
}

func TestExpandRefusesAHostThatDoesNotRouteBack(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		value   string
	}{
		{"an upper-case value", "{tenant}.example.com", "Acme"},
		{"a dot in one label", "{tenant}.example.com", "a.b"},
		{"a regex mismatch", "{tenant:[a-z]+}.example.com", "acme1"},
		{"an empty value", "{tenant}.example.com", ""},
		{"a port", "{tenant}.example.com", "acme:80"},
		{"a slash", "{tenant}.example.com", "a/b"},
		{"a space", "{tenant}.example.com", "a b"},
		{"a template value that carries the literal", "app-{tenant}.example.com", "x.y"},
		{"an empty rest label", "{tenant...}.example.com", ""},
		{"a rest label with an empty label", "{tenant...}.example.com", "a..b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Expand(tc.pattern, "tenant", tc.value); err == nil {
				t.Errorf("Expand(%q, %q) = %q, want an error", tc.pattern, tc.value, got)
			}
		})
	}

	for _, p := range []string{"*", "*.example.com", "example.com:8080", ""} {
		if got, err := Expand(p); err == nil {
			t.Errorf("Expand(%q) = %q, want an error", p, got)
		}
	}
}

func TestExpandedHostRoutesBack(t *testing.T) {
	r := newTestRouter()
	r.Host("{tenant}.example.com", func(h *Router[*tctx]) { h.GET("/", echoHost) })
	r.Host("app-{env}.example.com", func(h *Router[*tctx]) { h.GET("/", echoHost) })

	tests := []struct {
		pattern string
		pairs   []string
		want    string
	}{
		{"{tenant}.example.com", []string{"tenant", "acme"}, "{tenant}.example.com|/ tenant=acme"},
		{"app-{env}.example.com", []string{"env", "prod-eu"}, "app-{env}.example.com|/ env=prod-eu"},
	}
	for _, tc := range tests {
		t.Run(tc.pattern, func(t *testing.T) {
			host := MustExpand(tc.pattern, tc.pairs...)
			if got := doHost(r, http.MethodGet, host, "/").Body.String(); got != tc.want {
				t.Errorf("GET %s/ reached %q, want %q", host, got, tc.want)
			}
		})
	}
}

func TestMustExpandPanicsOnAMistake(t *testing.T) {
	if got := MustExpand("/blog/{year}/{slug}", "year", "2026", "slug", "hello"); got != "/blog/2026/hello" {
		t.Errorf("MustExpand = %q", got)
	}
	defer func() {
		if msg := fmt.Sprint(recover()); !strings.Contains(msg, `needs a value for "id"`) {
			t.Errorf("panic = %q, want one that names the missing parameter", msg)
		}
	}()
	MustExpand("/users/{id}")
}

func TestExpandCacheStaysBounded(t *testing.T) {
	t.Cleanup(func() {
		templateCache.Range(func(k, _ any) bool {
			if strings.HasPrefix(k.(string), "/cache-test/") {
				templateCache.Delete(k)
				cachedPatterns.Add(-1)
			}
			return true
		})
	})
	for i := range maxCachedTemplates + 10 {
		p := fmt.Sprintf("/cache-test/%d/{id}", i)
		if got := MustExpand(p, "id", "7"); got != fmt.Sprintf("/cache-test/%d/7", i) {
			t.Fatalf("Expand(%q) = %q", p, got)
		}
	}
	if n := cachedPatterns.Load(); n != maxCachedTemplates {
		t.Errorf("the cache holds %d patterns, want %d", n, maxCachedTemplates)
	}
	p := fmt.Sprintf("/cache-test/%d/{id}", maxCachedTemplates+5)
	if _, ok := templateCache.Load(p); ok {
		t.Error("a pattern past the cap was cached")
	}
	if got := MustExpand(p, "id", "8"); got != fmt.Sprintf("/cache-test/%d/8", maxCachedTemplates+5) {
		t.Errorf("an uncached pattern expanded to %q", got)
	}
	if _, err := Expand("/cache-test/{"); err == nil {
		t.Error("a malformed pattern expanded")
	}
}

// BenchmarkExpand measured two allocations on a cached pattern, and this keeps
// it from growing.
func TestExpandStaysAtTwoAllocations(t *testing.T) {
	const pattern = "/agents/{agent}/tickets/{id:int}"
	MustExpand(pattern, "agent", "ann", "id", "42")
	if allocs := testing.AllocsPerRun(100, func() { _, _ = Expand(pattern, "agent", "ann", "id", "42") }); allocs > 2 {
		t.Errorf("Expand made %v allocations, want at most 2", allocs)
	}
}
