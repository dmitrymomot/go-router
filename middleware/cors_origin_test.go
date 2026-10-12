package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func TestCORSComparesOriginsInCanonicalForm(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		sent       string
		want       bool
	}{
		{"https default port configured", "https://app.example:443", "https://app.example", true},
		{"http default port configured", "http://app.example:80", "http://app.example", true},
		{"default port sent", "https://app.example", "https://app.example:443", true},
		{"scheme and host in another case", "HTTPS://App.Example", "https://APP.example", true},
		{"a port that is not the default", "https://app.example:8443", "https://app.example:8443", true},
		{"the http default on https", "https://app.example:80", "https://app.example", false},
		{"the https default on http", "http://app.example:443", "http://app.example", false},
		{"another port", "https://app.example:8443", "https://app.example", false},
		{"an IPv6 host", "https://[::1]:443", "https://[::1]", true},
		{"the null origin", "https://app.example", "null", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := corsRouter(middleware.CORSConfig[*appContext]{AllowOrigins: []string{tt.configured}})
			req := httptest.NewRequest(http.MethodGet, "/data", nil)
			req.Header.Set(router.HeaderOrigin, tt.sent)
			got := do(r, req).Header().Get(router.HeaderAccessControlAllowOrigin) != ""
			if got != tt.want {
				t.Errorf("allowed = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCORSAllowPrivateNetwork(t *testing.T) {
	r := corsRouter(middleware.CORSConfig[*appContext]{
		AllowOrigins:        []string{"https://app.example"},
		AllowPrivateNetwork: true,
	})

	req := preflight("/data", http.MethodGet, "https://app.example")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	rec := do(r, req)
	if got := rec.Header().Get("Access-Control-Allow-Private-Network"); got != "true" {
		t.Errorf("allow private network = %q, want true", got)
	}
	if vary := strings.Join(rec.Header().Values(router.HeaderVary), ", "); !strings.Contains(vary, "Access-Control-Request-Private-Network") {
		t.Errorf("Vary = %q, want Access-Control-Request-Private-Network", vary)
	}

	rec = do(r, preflight("/data", http.MethodGet, "https://app.example"))
	if got := rec.Header().Get("Access-Control-Allow-Private-Network"); got != "" {
		t.Errorf("allow private network = %q on a preflight that did not ask, want none", got)
	}

	req = preflight("/data", http.MethodGet, "https://evil.example")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	if got := do(r, req).Header().Get("Access-Control-Allow-Private-Network"); got != "" {
		t.Errorf("allow private network = %q for an origin that is not allowed, want none", got)
	}
}

func TestCORSPrivateNetworkIsOffByDefault(t *testing.T) {
	r := corsRouter(middleware.CORSConfig[*appContext]{AllowOrigins: []string{"https://app.example"}})
	req := preflight("/data", http.MethodGet, "https://app.example")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	if got := do(r, req).Header().Get("Access-Control-Allow-Private-Network"); got != "" {
		t.Errorf("allow private network = %q, want none", got)
	}
}
