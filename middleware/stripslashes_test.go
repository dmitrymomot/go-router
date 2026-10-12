package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dmitrymomot/go-router/middleware"
)

func TestStripSlashes(t *testing.T) {
	var path, raw string
	h := middleware.StripSlashes(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		path, raw = r.URL.Path, r.URL.RawPath
	}))
	tests := []struct {
		target, path, raw string
	}{
		{"/users/", "/users", ""},
		{"/users///", "/users", ""},
		{"/users", "/users", ""},
		{"/", "/", ""},
		{"//", "/", ""},
		{"/a%2Fb/", "/a/b", "/a%2Fb"},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			orig := req.URL.Path
			do(h, req)
			if path != tt.path || raw != tt.raw {
				t.Errorf("got %q %q, want %q %q", path, raw, tt.path, tt.raw)
			}
			if req.URL.Path != orig {
				t.Error("the original request was changed")
			}
		})
	}
}

func TestStripSlashesFrontsTheRouter(t *testing.T) {
	r := newRouter()
	r.GET("/users", hello)
	if rec := get(middleware.StripSlashes(r), "/users/"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
