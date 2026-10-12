package middleware_test

import (
	"net/http"
	"testing"

	"github.com/dmitrymomot/go-router/middleware"
)

func TestSecureCrossOriginPolicies(t *testing.T) {
	const (
		coop = "Cross-Origin-Opener-Policy"
		coep = "Cross-Origin-Embedder-Policy"
		corp = "Cross-Origin-Resource-Policy"
		perm = "Permissions-Policy"
	)
	tests := []struct {
		name string
		cfg  middleware.SecureConfig[*appContext]
		want map[string]string
	}{
		{"defaults", middleware.SecureConfig[*appContext]{},
			map[string]string{coop: "same-origin", coep: "", corp: "same-origin", perm: ""}},
		{"set", middleware.SecureConfig[*appContext]{
			CrossOriginOpenerPolicy:   "same-origin-allow-popups",
			CrossOriginEmbedderPolicy: "require-corp",
			CrossOriginResourcePolicy: "cross-origin",
			PermissionsPolicy:         "camera=(), geolocation=(self)",
		}, map[string]string{
			coop: "same-origin-allow-popups", coep: "require-corp", corp: "cross-origin",
			perm: "camera=(), geolocation=(self)",
		}},
		{"omitted", middleware.SecureConfig[*appContext]{
			CrossOriginOpenerPolicy:   middleware.SecureOmit,
			CrossOriginResourcePolicy: middleware.SecureOmit,
			CrossOriginEmbedderPolicy: middleware.SecureOmit,
			PermissionsPolicy:         middleware.SecureOmit,
		}, map[string]string{coop: "", coep: "", corp: "", perm: ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := secureHeaders(secureRouter(tt.cfg), "https://app.example/")
			for name, want := range tt.want {
				if got := h.Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}

func TestSecureCrossOriginPoliciesKeepTheHandlerOverride(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Secure[*appContext])
	r.GET("/", func(c *appContext) error {
		c.SetHeader("Cross-Origin-Resource-Policy", "cross-origin")
		return c.String(http.StatusOK, "ok")
	})
	h := secureHeaders(r, "https://app.example/")
	if got := h.Get("Cross-Origin-Resource-Policy"); got != "cross-origin" {
		t.Errorf("Cross-Origin-Resource-Policy = %q, want the handler's value", got)
	}
}
