package router

import (
	"net/http"
	"testing"
)

// TestRadixSplitting exercises the shapes that node splitting produces. Each
// case registers patterns whose literal runs overlap, so the tree has to split
// a node and keep the children of the old one reachable.
func TestRadixSplitting(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		requests map[string]string // path -> matched pattern, or "" for 404
	}{
		{
			name:     "one literal is a prefix of another",
			patterns: []string{"/user", "/users", "/username"},
			requests: map[string]string{
				"/user":     "/user",
				"/users":    "/users",
				"/username": "/username",
				"/use":      "",
				"/userx":    "",
			},
		},
		{
			name:     "a split happens in the middle of a node",
			patterns: []string{"/abc", "/abd"},
			requests: map[string]string{"/abc": "/abc", "/abd": "/abd", "/ab": "", "/abcd": ""},
		},
		{
			name:     "a shorter literal arrives after a longer one",
			patterns: []string{"/abc", "/ab"},
			requests: map[string]string{"/abc": "/abc", "/ab": "/ab", "/abd": ""},
		},
		{
			name:     "a split moves a parameter child down",
			patterns: []string{"/users/{id}", "/user/{name}"},
			requests: map[string]string{
				"/users/7":   "/users/{id}",
				"/user/bob":  "/user/{name}",
				"/users":     "",
				"/users/7/x": "",
			},
		},
		{
			name:     "a literal sibling of a parameter",
			patterns: []string{"/v1/users", "/v1/users/{id}", "/v1/usersettings"},
			requests: map[string]string{
				"/v1/users":        "/v1/users",
				"/v1/users/7":      "/v1/users/{id}",
				"/v1/usersettings": "/v1/usersettings",
				"/v1/usersfoo":     "",
			},
		},
		{
			name:     "a catch-all needs a separator in front of it",
			patterns: []string{"/assets/*", "/assetsfoo"},
			requests: map[string]string{
				"/assets/a/b.css": "/assets/*",
				"/assets":         "/assets/*",
				"/assetsfoo":      "/assetsfoo",
				"/assetsbar":      "",
			},
		},
		{
			name:     "the walk backtracks out of a dead literal branch",
			patterns: []string{"/a/{x}/c", "/a/b/d"},
			requests: map[string]string{
				"/a/b/c": "/a/{x}/c",
				"/a/b/d": "/a/b/d",
				"/a/z/c": "/a/{x}/c",
				"/a/b/e": "",
			},
		},
		{
			name:     "a literal branch that dies deeper than the parameter",
			patterns: []string{"/p/{name}", "/p/abc/def"},
			requests: map[string]string{
				"/p/abc":     "/p/{name}",
				"/p/abc/def": "/p/abc/def",
				"/p/abcd":    "/p/{name}",
				"/p/abc/x":   "",
			},
		},
		{
			name:     "the root shares its node with everything",
			patterns: []string{"/", "/x", "/{y}"},
			requests: map[string]string{"/": "/", "/x": "/x", "/z": "/{y}"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRouter()
			for _, p := range tc.patterns {
				r.GET(p, func(c *tctx) error { return c.String(http.StatusOK, c.RoutePattern()) })
			}
			for path, want := range tc.requests {
				rec := do(r, http.MethodGet, path)
				switch {
				case want == "":
					if rec.Code != http.StatusNotFound {
						t.Errorf("%s: status = %d, want 404 (matched %q)", path, rec.Code, rec.Body)
					}
				case rec.Code != http.StatusOK:
					t.Errorf("%s: status = %d, want 200", path, rec.Code)
				case rec.Body.String() != want:
					t.Errorf("%s: matched %q, want %q", path, rec.Body.String(), want)
				}
			}
		})
	}
}

// TestRadixKeepsRoutesAcrossASplit checks that splitting a node does not lose
// the methods or the parameter names that the old node carried.
func TestRadixKeepsRoutesAcrossASplit(t *testing.T) {
	r := newTestRouter()
	r.GET("/orders/{id}", echoRoute)
	r.POST("/orders/{id}", echoRoute)
	// Registering this splits the /orders node that already holds both routes.
	r.GET("/order", echoRoute)

	for _, tc := range []struct {
		method, path, want string
	}{
		{http.MethodGet, "/orders/7", "/orders/{id} id=7"},
		{http.MethodPost, "/orders/7", "/orders/{id} id=7"},
		{http.MethodGet, "/order", "/order"},
	} {
		rec := do(r, tc.method, tc.path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: status = %d, want 200", tc.method, tc.path, rec.Code)
		}
		if got := rec.Body.String(); got != tc.want {
			t.Errorf("%s %s: body = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}

	if rec := do(r, http.MethodDelete, "/orders/7"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	} else if got := rec.Header().Get(HeaderAllow); got != "GET, HEAD, OPTIONS, POST" {
		t.Errorf("Allow = %q", got)
	}
}
