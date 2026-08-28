package router

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func namedRouter() *Router[*tctx] {
	r := newTestRouter()
	r.Name("home").GET("/", echoRoute)
	r.Name("user").GET("/users/{id}", echoRoute)
	r.Name("order").GET("/orders/{id:[0-9]+}", echoRoute)
	r.Name("post").GET("/blog/{year}/{slug}", echoRoute)
	r.Name("report").GET("/reports/rep-{date}.csv", echoRoute)
	r.Name("split").GET("/files/{name}.{ext}", echoRoute)
	r.Name("tree").GET("/tree/{path...}", echoRoute)
	r.Name("asset").GET("/assets/*", echoRoute)
	r.Route("/v1", func(g *Router[*tctx]) {
		g.Name("v1.user").GET("/users/{id}", echoRoute)
	})
	return r
}

func TestURLFillsInEveryPatternShape(t *testing.T) {
	r := namedRouter()

	tests := []struct {
		name   string
		route  string
		params map[string]string
		want   string
	}{
		{"a static route", "home", nil, "/"},
		{"one parameter", "user", map[string]string{"id": "7"}, "/users/7"},
		{"a regular expression parameter", "order", map[string]string{"id": "42"}, "/orders/42"},
		{"two parameters", "post", map[string]string{"year": "2026", "slug": "hello"}, "/blog/2026/hello"},
		{"a partial segment", "report", map[string]string{"date": "20260102"}, "/reports/rep-20260102.csv"},
		{"two parameters in one segment", "split", map[string]string{"name": "notes", "ext": "txt"}, "/files/notes.txt"},
		{"a catch-all", "tree", map[string]string{"path": "a/b/c.txt"}, "/tree/a/b/c.txt"},
		{"an empty catch-all", "tree", map[string]string{"path": ""}, "/tree"},
		{"an anonymous catch-all", "asset", map[string]string{"*": "css/app.css"}, "/assets/css/app.css"},
		{"a route of a prefix scope", "v1.user", map[string]string{"id": "7"}, "/v1/users/7"},
		{"a value that needs escaping", "user", map[string]string{"id": "a b/c"}, "/users/a%20b%2Fc"},
		{"a catch-all keeps its separators", "tree", map[string]string{"path": "a b/c d"}, "/tree/a%20b/c%20d"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.URL(tc.route, tc.params)
			if err != nil {
				t.Fatalf("URL(%q) = %v", tc.route, err)
			}
			if got != tc.want {
				t.Errorf("URL(%q) = %q, want %q", tc.route, got, tc.want)
			}
		})
	}
}

func TestURLBuildsAPathThatRoutesBack(t *testing.T) {
	r := namedRouter()

	tests := []struct {
		route  string
		params map[string]string
		want   string
	}{
		{"home", nil, "/"},
		{"user", map[string]string{"id": "7"}, "/users/{id} id=7"},
		{"user", map[string]string{"id": "a b"}, "/users/{id} id=a b"},
		{"post", map[string]string{"year": "2026", "slug": "hello"}, "/blog/{year}/{slug} year=2026 slug=hello"},
		{"report", map[string]string{"date": "20260102"}, "/reports/rep-{date}.csv date=20260102"},
		{"tree", map[string]string{"path": "a/b.txt"}, "/tree/{path...} path=a/b.txt"},
		{"tree", map[string]string{"path": ""}, "/tree/{path...} path="},
		{"v1.user", map[string]string{"id": "7"}, "/v1/users/{id} id=7"},
	}
	for _, tc := range tests {
		t.Run(tc.route, func(t *testing.T) {
			path, err := r.URL(tc.route, tc.params)
			if err != nil {
				t.Fatalf("URL(%q) = %v", tc.route, err)
			}
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

func TestURLReportsABadCall(t *testing.T) {
	r := namedRouter()

	tests := []struct {
		name   string
		route  string
		params map[string]string
		want   string
	}{
		{"an unknown name", "nope", nil, `no route is named "nope"`},
		{"a missing parameter", "user", nil, `needs the parameter "id"`},
		{"one of two missing", "post", map[string]string{"year": "2026"}, `needs the parameter "slug"`},
		{"a spare parameter", "user", map[string]string{"id": "7", "page": "2"}, `takes no parameter "page"`},
		{"only spare parameters", "home", map[string]string{"a": "1", "b": "2"}, `takes no parameter "a", "b"`},
		{"an empty value", "user", map[string]string{"id": ""}, `is empty`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.URL(tc.route, tc.params)
			if err == nil {
				t.Fatalf("URL(%q) = %q, want an error that mentions %q", tc.route, got, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("URL(%q) = %v, want an error that mentions %q", tc.route, err, tc.want)
			}
		})
	}
}

func TestMustURLTakesAlternatingKeysAndValues(t *testing.T) {
	r := namedRouter()

	if got := r.MustURL("home"); got != "/" {
		t.Errorf("MustURL(home) = %q, want %q", got, "/")
	}
	if got := r.MustURL("post", "year", "2026", "slug", "hello"); got != "/blog/2026/hello" {
		t.Errorf("MustURL(post) = %q, want %q", got, "/blog/2026/hello")
	}
}

func TestMustURLPanicsOnAMistake(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
		want string
	}{
		{"an unknown name", func() { namedRouter().MustURL("nope") }, "no route is named"},
		{"a missing parameter", func() { namedRouter().MustURL("user") }, "needs the parameter"},
		{"a spare parameter", func() { namedRouter().MustURL("user", "id", "7", "page", "2") }, "takes no parameter"},
		//nolint:staticcheck // SA5012: the odd argument count is what this case proves.
		{"an odd argument count", func() { namedRouter().MustURL("user", "id") }, "alternating keys and values"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				got := recover()
				if got == nil {
					t.Fatalf("no panic, want one that mentions %q", tc.want)
				}
				if msg := fmt.Sprint(got); !strings.Contains(msg, tc.want) {
					t.Errorf("panic = %q, want one that mentions %q", msg, tc.want)
				}
			}()
			tc.fn()
		})
	}
}

func TestURLReportsABrokenRouteTable(t *testing.T) {
	r := newTestRouter()
	r.Name("dup").GET("/a", echoRoute)
	r.GET("/a", echoRoute)

	if _, err := r.URL("dup", nil); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Errorf("URL = %v, want the conflict that Build reports", err)
	}
}

func TestNamedRouteOfAHostScopeResolvesToItsPath(t *testing.T) {
	r := newTestRouter()
	r.Hosts([]string{"{tenant}.example.com", "*"}, func(h *Router[*tctx]) {
		h.Name("dashboard").GET("/dashboard/{tab}", echoRoute)
	})

	got, err := r.URL("dashboard", map[string]string{"tab": "usage"})
	if err != nil {
		t.Fatalf("URL = %v", err)
	}
	if want := "/dashboard/usage"; got != want {
		t.Errorf("URL = %q, want %q", got, want)
	}
}

func TestParseURLTemplateCutsAPattern(t *testing.T) {
	tests := []struct {
		pattern string
		want    []urlPart
	}{
		{"/", []urlPart{{lit: "/"}}},
		{"/users", []urlPart{{lit: "/users"}}},
		{"/users/{id}", []urlPart{{lit: "/users/"}, {name: "id"}}},
		{"/orders/{id:[0-9]+}", []urlPart{{lit: "/orders/"}, {name: "id"}}},
		{"/reports/rep-{date}.csv", []urlPart{{lit: "/reports/rep-"}, {name: "date"}, {lit: ".csv"}}},
		{"/files/{name}.{ext}", []urlPart{{lit: "/files/"}, {name: "name"}, {lit: "."}, {name: "ext"}}},
		{"/tree/{path...}", []urlPart{{lit: "/tree/"}, {name: "path", rest: true}}},
		{"/assets/*", []urlPart{{lit: "/assets/"}, {name: "*", rest: true}}},
		{"/a/{x}/b/{y}", []urlPart{{lit: "/a/"}, {name: "x"}, {lit: "/b/"}, {name: "y"}}},
	}
	for _, tc := range tests {
		t.Run(tc.pattern, func(t *testing.T) {
			got := parseURLTemplate(tc.pattern)
			if len(got) != len(tc.want) {
				t.Fatalf("parseURLTemplate(%q) = %v, want %v", tc.pattern, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("part %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestURLRefusesAValueThatReadsBackAsAnotherOne(t *testing.T) {
	r := newTestRouter()
	r.Name("rep").GET("/r/{env}-{name}", echoRoute)
	r.Name("rest").GET("/f/{p...}", echoRoute)

	tests := []struct {
		name   string
		route  string
		params map[string]string
	}{
		{"a value that carries the literal of its segment", "rep", map[string]string{"env": "prod", "name": "web-api"}},
		{"a catch-all that is nothing but a separator", "rest", map[string]string{"p": "/"}},
		{"a catch-all with a trailing separator", "rest", map[string]string{"p": "x/"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.URL(tc.route, tc.params)
			if err == nil {
				t.Fatalf("URL(%q) = %q, want an error; the path reads back as other values", tc.route, got)
			}
		})
	}
}

func TestURLRefusesAValueThePatternRejects(t *testing.T) {
	r := namedRouter()
	r.Name("tmpl").GET("/reports/rep-{date:[0-9]{8}}.csv", echoRoute)

	tests := []struct {
		name   string
		route  string
		params map[string]string
	}{
		{"a whole segment", "order", map[string]string{"id": "abc"}},
		{"a template segment", "tmpl", map[string]string{"date": "nope"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.URL(tc.route, tc.params)
			if err == nil {
				t.Fatalf("URL(%q) = %q, want an error; the pattern rejects the value", tc.route, got)
			}
		})
	}
}

func TestURLBuildsAPathThatRoutesBackForEveryShape(t *testing.T) {
	r := newTestRouter()
	r.Name("rep").GET("/r/{env}-{name}", echoRoute)
	r.Name("rest").GET("/f/{p...}", echoRoute)
	r.Name("split").GET("/s/{name}.{ext}", echoRoute)

	tests := []struct {
		route  string
		params map[string]string
		want   string
	}{
		{"rep", map[string]string{"env": "prod", "name": "web"}, "/r/{env}-{name} env=prod name=web"},
		{"rep", map[string]string{"env": "pre-prod", "name": "web"}, "/r/{env}-{name} env=pre-prod name=web"},
		{"split", map[string]string{"name": "a.b", "ext": "txt"}, "/s/{name}.{ext} name=a.b ext=txt"},
		{"rest", map[string]string{"p": "a/b.txt"}, "/f/{p...} p=a/b.txt"},
		{"rest", map[string]string{"p": "/x"}, "/f/{p...} p=/x"},
		{"rest", map[string]string{"p": ""}, "/f/{p...} p="},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.route, tc.params), func(t *testing.T) {
			path, err := r.URL(tc.route, tc.params)
			if err != nil {
				t.Fatalf("URL(%q) = %v", tc.route, err)
			}
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
