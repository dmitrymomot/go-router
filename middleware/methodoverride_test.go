package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router/middleware"
)

func TestMethodOverride(t *testing.T) {
	var method string
	var field string
	h := middleware.MethodOverride(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		method = r.Method
		field = r.PostFormValue("name")
	}))
	form := func(m string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(url.Values{
			"_method": {m}, "name": {"x"},
		}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req
	}
	withHeader := func(m string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set(middleware.HeaderMethodOverride, m)
		return req
	}
	tests := []struct {
		name string
		req  *http.Request
		want string
	}{
		{"header put", withHeader("PUT"), "PUT"},
		{"header lower patch", withHeader("patch"), "PATCH"},
		{"header delete", withHeader("DELETE"), "DELETE"},
		{"header get is ignored", withHeader("GET"), "POST"},
		{"header junk is ignored", withHeader("BREW"), "POST"},
		{"form delete", form("delete"), "DELETE"},
		{"form connect is ignored", form("CONNECT"), "POST"},
		{"get is never changed", func() *http.Request {
			req := httptest.NewRequest(http.MethodGet, "/?_method=DELETE", nil)
			req.Header.Set(middleware.HeaderMethodOverride, "DELETE")
			return req
		}(), "GET"},
		{"query is not read", httptest.NewRequest(http.MethodPost, "/?_method=DELETE", nil), "POST"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method, field = "", ""
			do(h, tt.req)
			if method != tt.want {
				t.Errorf("method = %q, want %q", method, tt.want)
			}
		})
	}

	do(h, form("PUT"))
	if field != "x" {
		t.Errorf("the form was not left readable: name = %q", field)
	}
}

func TestMethodOverrideReachesTheRoute(t *testing.T) {
	r := newRouter()
	r.DELETE("/items/1", hello)
	req := httptest.NewRequest(http.MethodPost, "/items/1", nil)
	req.Header.Set(middleware.HeaderMethodOverride, "DELETE")
	if rec := do(middleware.MethodOverride(r), req); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
