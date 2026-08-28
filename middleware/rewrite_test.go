package middleware_test

import (
	"net/http"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func rewriteRouter(path *string, rules ...middleware.RewriteRule) *router.Router[*appContext] {
	r := newRouter()
	r.Pre(middleware.Rewrite[*appContext](rules...))
	r.GET("/{rest...}", func(c *appContext) error {
		*path = c.Request().URL.Path
		return c.NoContent(http.StatusOK)
	})
	return r
}

func TestRewriteRewritesThePathBeforeTheMatch(t *testing.T) {
	r := newRouter()
	r.Pre(middleware.Rewrite[*appContext](
		middleware.RewriteRule{Match: "/api/v1/*", To: "/v1/$1"},
	))
	r.GET("/v1/users/{id}", func(c *appContext) error {
		return c.String(http.StatusOK, c.Param("id"))
	})

	rec := get(r, "/api/v1/users/7")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: the rewritten path never reached the trie", rec.Code)
	}
	if rec.Body.String() != "7" {
		t.Errorf("id = %q, want 7", rec.Body.String())
	}
}

func TestRewriteMatching(t *testing.T) {
	tests := []struct {
		name  string
		match string
		to    string
		path  string
		want  string
	}{
		{name: "a trailing wildcard", match: "/old/*", to: "/new/$1", path: "/old/a/b", want: "/new/a/b"},
		{name: "a wildcard that took nothing", match: "/old/*", to: "/new/$1", path: "/old/", want: "/new/"},
		{name: "no wildcard", match: "/exact", to: "/moved", path: "/exact", want: "/moved"},
		{name: "no wildcard and no match", match: "/exact", to: "/moved", path: "/exactly", want: "/exactly"},
		{name: "two wildcards", match: "/*/edit/*", to: "/$2/$1", path: "/posts/edit/7", want: "/7/posts"},
		{name: "a literal after the wildcard", match: "/files/*.txt", to: "/docs/$1", path: "/files/a.b.txt", want: "/docs/a.b"},
		{name: "a literal that the path lacks", match: "/files/*.txt", to: "/docs/$1", path: "/files/a.md", want: "/files/a.md"},
		{name: "a capture that no wildcard fills", match: "/old/*", to: "/new/$2", path: "/old/x", want: "/new/"},
		{name: "a path that no rule matches", match: "/old/*", to: "/new/$1", path: "/other", want: "/other"},
		{name: "a wildcard in the middle", match: "/x/*/y/*", to: "/$1/$2", path: "/x/a/y/b/y/c", want: "/a/b/y/c"},
		{name: "a replacement with no capture", match: "/old/*", to: "/new", path: "/old/a", want: "/new"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			r := rewriteRouter(&got, middleware.RewriteRule{Match: tc.match, To: tc.to})
			if rec := get(r, tc.path); rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got != tc.want {
				t.Errorf("path = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRewriteTakesTheFirstRuleThatMatches(t *testing.T) {
	var got string
	r := rewriteRouter(&got,
		middleware.RewriteRule{Match: "/a/*", To: "/first/$1"},
		middleware.RewriteRule{Match: "/a/b", To: "/second"},
	)

	get(r, "/a/b")
	if got != "/first/b" {
		t.Errorf("path = %q, want the rewrite of the first rule that matched", got)
	}
}

func TestRewriteRewritesOnce(t *testing.T) {
	var got string
	r := rewriteRouter(&got,
		middleware.RewriteRule{Match: "/a/*", To: "/b/$1"},
		middleware.RewriteRule{Match: "/b/*", To: "/c/$1"},
	)

	get(r, "/a/x")
	if got != "/b/x" {
		t.Errorf("path = %q, want /b/x: the result went back through the rules", got)
	}
}

func TestRewriteLeavesTheQueryAlone(t *testing.T) {
	r := newRouter()
	r.Pre(middleware.Rewrite[*appContext](
		middleware.RewriteRule{Match: "/old/*", To: "/new/$1"},
	))
	r.GET("/new/{name}", func(c *appContext) error {
		return c.Stringf(http.StatusOK, "%s?%s", c.Request().URL.Path, c.Request().URL.RawQuery)
	})

	if got := get(r, "/old/page?q=1&page=2").Body.String(); got != "/new/page?q=1&page=2" {
		t.Errorf("url = %q, want the query as it arrived", got)
	}
}

func TestRewriteMatchesEscapedUnreservedCharacters(t *testing.T) {
	tests := []struct {
		name   string
		match  string
		to     string
		target string
		want   string
	}{
		{name: "literal", match: "/old/abc", to: "/new", target: "/old/%61bc", want: "/new"},
		{name: "wildcard", match: "/old/a*c", to: "/new/$1", target: "/old/%61b%63", want: "/new/b"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			r := rewriteRouter(&got, middleware.RewriteRule{Match: tc.match, To: tc.to})
			if rec := get(r, tc.target); rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got != tc.want {
				t.Errorf("path = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRewriteDoesNotMatchInsidePercentEscapes(t *testing.T) {
	tests := []struct {
		name   string
		match  string
		target string
	}{
		{name: "unreserved triplet", match: "/old/*4A", target: "/old/%4A"},
		{name: "reserved triplet suffix", match: "/old/*A", target: "/old/%2A"},
		{name: "reserved triplet middle", match: "/old/*2*", target: "/old/%2F"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			r := newRouter()
			r.Pre(middleware.Rewrite[*appContext](
				middleware.RewriteRule{Match: tc.match, To: "/new"},
			))
			r.GET("/{rest...}", func(c *appContext) error {
				got = c.Request().URL.EscapedPath()
				return c.NoContent(http.StatusOK)
			})

			if rec := get(r, tc.target); rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got != tc.target {
				t.Errorf("escaped path = %q, want unchanged %q", got, tc.target)
			}
		})
	}
}

func TestRewritePreservesEscapedSeparatorsAsParameterData(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		wantRaw   string
		wantParam string
	}{
		{name: "slash", target: "/old/a%2Fb", wantRaw: "/new/a%2Fb", wantParam: "a/b"},
		{name: "lowercase slash", target: "/old/a%2fb", wantRaw: "/new/a%2Fb", wantParam: "a/b"},
		{name: "backslash", target: "/old/a%5Cb", wantRaw: "/new/a%5Cb", wantParam: `a\b`},
		{name: "double escaped slash", target: "/old/a%252Fb", wantRaw: "/new/a%252Fb", wantParam: "a%2Fb"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRouter()
			r.Pre(middleware.Rewrite[*appContext](
				middleware.RewriteRule{Match: "/old/*", To: "/new/$1"},
			))
			r.GET("/new/{id}", func(c *appContext) error {
				if got := c.Request().URL.EscapedPath(); got != tc.wantRaw {
					t.Errorf("escaped path = %q, want %q", got, tc.wantRaw)
				}
				return c.String(http.StatusOK, c.Param("id"))
			})
			r.GET("/new/a/b", func(c *appContext) error {
				return c.String(http.StatusTeapot, "separator became route structure")
			})

			rec := get(r, tc.target)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
			}
			if got := rec.Body.String(); got != tc.wantParam {
				t.Errorf("parameter = %q, want %q", got, tc.wantParam)
			}
		})
	}
}

func TestRewriteStillMatchesDecodedUnicodeText(t *testing.T) {
	var got string
	r := rewriteRouter(&got, middleware.RewriteRule{Match: "/café/*", To: "/résumé/$1"})

	rec := get(r, "/caf%C3%A9/ada")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got != "/résumé/ada" {
		t.Errorf("path = %q, want %q", got, "/résumé/ada")
	}
}

func TestRewritePanicsOnARuleWithoutAMatch(t *testing.T) {
	mustPanicContaining(t, "Match", func() {
		middleware.Rewrite[*appContext](middleware.RewriteRule{To: "/new"})
	})
}
