package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func secureRouter(cfg middleware.SecureConfig) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.SecureWithConfig[*appContext](cfg))
	r.GET("/", func(c *appContext) error { return c.String(http.StatusOK, "ok") })
	return r
}

func secureHeaders(r http.Handler, target string) http.Header {
	return do(r, httptest.NewRequest(http.MethodGet, target, nil)).Header()
}

func TestSecureDefaults(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Secure[*appContext])
	r.GET("/", func(c *appContext) error { return c.String(http.StatusOK, "ok") })

	h := secureHeaders(r, "https://app.example/")
	tests := []struct {
		header string
		want   string
	}{
		{router.HeaderXContentTypeOptions, "nosniff"},
		{router.HeaderXFrameOptions, "SAMEORIGIN"},
		{router.HeaderReferrerPolicy, "strict-origin-when-cross-origin"},
		{router.HeaderContentSecurityPolicy, ""},
		{router.HeaderContentSecurityPolicyReportOnly, ""},
		{router.HeaderStrictTransportSecurity, ""},
	}
	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			if got := h.Get(tt.header); got != tt.want {
				t.Errorf("%s = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestSecureSendsNoXSSProtection(t *testing.T) {
	h := secureHeaders(secureRouter(middleware.SecureConfig{}), "https://app.example/")
	if got := h.Get("X-XSS-Protection"); got != "" {
		t.Errorf("X-XSS-Protection = %q, want none", got)
	}
}

func TestSecureOverridesTheDefaults(t *testing.T) {
	r := secureRouter(middleware.SecureConfig{
		ContentTypeNosniff: "nosniff",
		FrameOptions:       "DENY",
		ReferrerPolicy:     "no-referrer",
	})

	h := secureHeaders(r, "/")
	if got := h.Get(router.HeaderXFrameOptions); got != "DENY" {
		t.Errorf("frame options = %q, want %q", got, "DENY")
	}
	if got := h.Get(router.HeaderReferrerPolicy); got != "no-referrer" {
		t.Errorf("referrer policy = %q, want %q", got, "no-referrer")
	}
}

func TestSecureOmitDropsAHeader(t *testing.T) {
	tests := []struct {
		name   string
		cfg    middleware.SecureConfig
		header string
	}{
		{
			name:   "nosniff",
			cfg:    middleware.SecureConfig{ContentTypeNosniff: middleware.SecureOmit},
			header: router.HeaderXContentTypeOptions,
		},
		{
			name:   "frame options",
			cfg:    middleware.SecureConfig{FrameOptions: middleware.SecureOmit},
			header: router.HeaderXFrameOptions,
		},
		{
			name:   "referrer policy",
			cfg:    middleware.SecureConfig{ReferrerPolicy: middleware.SecureOmit},
			header: router.HeaderReferrerPolicy,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := secureHeaders(secureRouter(tt.cfg), "/")
			if got := h.Get(tt.header); got != "" {
				t.Errorf("%s = %q, want none", tt.header, got)
			}
			if got := h.Get(router.HeaderReferrerPolicy); got == "" && tt.header != router.HeaderReferrerPolicy {
				t.Error("the referrer policy went away with it")
			}
		})
	}
}

func TestSecureContentSecurityPolicy(t *testing.T) {
	const policy = "default-src 'self'"

	h := secureHeaders(secureRouter(middleware.SecureConfig{ContentSecurityPolicy: policy}), "/")
	if got := h.Get(router.HeaderContentSecurityPolicy); got != policy {
		t.Errorf("policy = %q, want %q", got, policy)
	}
	if got := h.Get(router.HeaderContentSecurityPolicyReportOnly); got != "" {
		t.Errorf("report-only policy = %q, want none", got)
	}
}

func TestSecureContentSecurityPolicyReportOnly(t *testing.T) {
	const policy = "default-src 'self'"

	h := secureHeaders(secureRouter(middleware.SecureConfig{
		ContentSecurityPolicy: policy,
		CSPReportOnly:         true,
	}), "/")
	if got := h.Get(router.HeaderContentSecurityPolicyReportOnly); got != policy {
		t.Errorf("report-only policy = %q, want %q", got, policy)
	}
	if got := h.Get(router.HeaderContentSecurityPolicy); got != "" {
		t.Errorf("policy = %q, want none: it reports and blocks nothing", got)
	}
}

func TestSecureHSTSValue(t *testing.T) {
	tests := []struct {
		name string
		cfg  middleware.SecureConfig
		want string
	}{
		{
			name: "an age alone",
			cfg:  middleware.SecureConfig{HSTSMaxAge: 24 * time.Hour},
			want: "max-age=86400",
		},
		{
			name: "with the subdomains",
			cfg: middleware.SecureConfig{
				HSTSMaxAge:            24 * time.Hour,
				HSTSIncludeSubdomains: true,
			},
			want: "max-age=86400; includeSubDomains",
		},
		{
			name: "with the preload token",
			cfg: middleware.SecureConfig{
				HSTSMaxAge:            365 * 24 * time.Hour,
				HSTSIncludeSubdomains: true,
				HSTSPreload:           true,
			},
			want: "max-age=31536000; includeSubDomains; preload",
		},
		{
			name: "no age sends nothing",
			cfg:  middleware.SecureConfig{HSTSIncludeSubdomains: true, HSTSPreload: true},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := secureHeaders(secureRouter(tt.cfg), "https://app.example/")
			if got := h.Get(router.HeaderStrictTransportSecurity); got != tt.want {
				t.Errorf("strict transport security = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSecureHSTSNeedsHTTPS(t *testing.T) {
	r := secureRouter(middleware.SecureConfig{HSTSMaxAge: 24 * time.Hour})

	if got := secureHeaders(r, "http://app.example/").Get(router.HeaderStrictTransportSecurity); got != "" {
		t.Errorf("strict transport security = %q, want none over plaintext", got)
	}
	if got := secureHeaders(r, "https://app.example/").Get(router.HeaderStrictTransportSecurity); got == "" {
		t.Error("strict transport security is missing from the TLS answer")
	}
}

func TestSecureHSTSReadsTheForwardedScheme(t *testing.T) {
	r := secureRouter(middleware.SecureConfig{HSTSMaxAge: 24 * time.Hour})

	req := httptest.NewRequest(http.MethodGet, "http://app.example/", nil)
	req.Header.Set(router.HeaderXForwardedProto, "https, http")
	if got := do(r, req).Header().Get(router.HeaderStrictTransportSecurity); got == "" {
		t.Error("strict transport security is missing behind a proxy that terminated TLS")
	}

	req = httptest.NewRequest(http.MethodGet, "http://app.example/", nil)
	req.Header.Set(router.HeaderXForwardedProto, "http")
	if got := do(r, req).Header().Get(router.HeaderStrictTransportSecurity); got != "" {
		t.Errorf("strict transport security = %q, want none behind a plaintext hop", got)
	}
}

func TestSecureHeadersSurviveAnError(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Secure[*appContext])
	r.GET("/boom", func(*appContext) error { return router.ErrNotFound })

	rec := get(r, "/boom")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get(router.HeaderXContentTypeOptions); got != "nosniff" {
		t.Errorf("content type options = %q, want %q", got, "nosniff")
	}
}

func TestSecureSkip(t *testing.T) {
	r := secureRouter(middleware.SecureConfig{Skip: skipPath("/")})
	if got := secureHeaders(r, "/").Get(router.HeaderXContentTypeOptions); got != "" {
		t.Errorf("content type options = %q, want none", got)
	}
}

func TestSecureRejectsASubSecondHSTSMaxAge(t *testing.T) {
	tests := []struct {
		name string
		age  time.Duration
	}{
		{"the number of seconds that every guide quotes", 31536000},
		{"half a second", 500 * time.Millisecond},
		{"a nanosecond under a second", time.Second - 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustPanicContaining(t, "HSTSMaxAge", func() {
				middleware.SecureWithConfig[*appContext](middleware.SecureConfig{
					HSTSMaxAge:            tt.age,
					HSTSIncludeSubdomains: true,
					HSTSPreload:           true,
				})
			})
		})
	}
}

func TestSecureTakesAWholeSecondHSTSMaxAge(t *testing.T) {
	h := secureHeaders(secureRouter(middleware.SecureConfig{HSTSMaxAge: time.Second}), "https://app.example/")
	if got := h.Get(router.HeaderStrictTransportSecurity); got != "max-age=1" {
		t.Errorf("strict transport security = %q, want %q", got, "max-age=1")
	}
}
