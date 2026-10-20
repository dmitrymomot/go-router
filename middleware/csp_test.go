package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func TestSecureRendersATypedPolicy(t *testing.T) {
	h := secureHeaders(secureRouter(middleware.SecureConfig[*appContext]{CSP: &middleware.CSPPolicy{
		DefaultSrc:              []string{middleware.CSPSelf},
		ScriptSrc:               []string{middleware.CSPSelf, "https://cdn.example"},
		StyleSrc:                []string{middleware.CSPSelf, middleware.CSPUnsafeInline},
		ImgSrc:                  []string{middleware.CSPSelf, middleware.CSPData, middleware.CSPBlob},
		ConnectSrc:              []string{middleware.CSPSelf, "wss://app.example"},
		FontSrc:                 []string{"https://fonts.example"},
		MediaSrc:                []string{middleware.CSPNone},
		ObjectSrc:               []string{middleware.CSPNone},
		FrameSrc:                []string{"https://pay.example"},
		WorkerSrc:               []string{middleware.CSPSelf},
		ManifestSrc:             []string{middleware.CSPSelf},
		FrameAncestors:          []string{middleware.CSPNone},
		BaseURI:                 []string{middleware.CSPSelf},
		FormAction:              []string{middleware.CSPSelf},
		UpgradeInsecureRequests: true,
		ReportURI:               "/csp-reports",
		ReportTo:                "csp",
	}}), "https://app.example/")
	want := "default-src 'self'; script-src 'self' https://cdn.example; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob:; connect-src 'self' wss://app.example; font-src https://fonts.example; " +
		"media-src 'none'; object-src 'none'; frame-src https://pay.example; worker-src 'self'; " +
		"manifest-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; " +
		"upgrade-insecure-requests; report-uri /csp-reports; report-to csp"
	if got := h.Get(router.HeaderContentSecurityPolicy); got != want {
		t.Errorf("policy =\n%s\nwant\n%s", got, want)
	}
}

func TestSecureMintsANoncePerRequest(t *testing.T) {
	r := newRouter()
	r.Use(middleware.SecureWithConfig(middleware.SecureConfig[*appContext]{
		CSP: &middleware.CSPPolicy{
			DefaultSrc: []string{middleware.CSPSelf},
			ScriptSrc:  []string{middleware.CSPStrictDynamic},
		},
		CSPWithNonce:  true,
		CSPReportOnly: true,
	}))
	r.GET("/", func(c *appContext) error {
		// Every context of the request reads the same nonce.
		n := middleware.CSPNonce(c)
		if middleware.CSPNonce(c.Request().Context()) != n || middleware.CSPNonce(context.WithoutCancel(c)) != n {
			return c.String(http.StatusInternalServerError, "mismatch")
		}
		return c.String(http.StatusOK, n)
	})

	shape := regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)
	seen := map[string]bool{}
	for range 3 {
		rec := do(r, httptest.NewRequest(http.MethodGet, "/", nil))
		nonce := rec.Body.String()
		if !shape.MatchString(nonce) || seen[nonce] {
			t.Fatalf("nonce = %q, want a fresh 16 bytes of base64url", nonce)
		}
		seen[nonce] = true
		want := "default-src 'self'; script-src 'strict-dynamic' 'nonce-" + nonce + "'; style-src 'nonce-" + nonce + "'"
		if got := rec.Header().Get(router.HeaderContentSecurityPolicyReportOnly); got != want {
			t.Errorf("policy = %q, want %q", got, want)
		}
	}
}

// templNonceKey stands in for the context key of a template engine.
type templNonceKey struct{}

func TestSecurePassesTheNonceToCSPNonceContext(t *testing.T) {
	calls := 0
	r := newRouter()
	r.Use(middleware.SecureWithConfig(middleware.SecureConfig[*appContext]{
		CSP:          &middleware.CSPPolicy{DefaultSrc: []string{middleware.CSPSelf}},
		CSPWithNonce: true,
		CSPNonceContext: func(ctx context.Context, nonce string) context.Context {
			calls++
			return context.WithValue(ctx, templNonceKey{}, nonce)
		},
	}))
	r.GET("/", func(c *appContext) error {
		n, _ := c.Value(templNonceKey{}).(string)
		if n == "" || n != middleware.CSPNonce(c) {
			return c.String(http.StatusInternalServerError, "mismatch")
		}
		return c.String(http.StatusOK, n)
	})

	rec := do(r, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status = %d after %d calls, want 200 after 1", rec.Code, calls)
	}
	if got := rec.Header().Get(router.HeaderContentSecurityPolicy); !strings.Contains(got, "'nonce-"+rec.Body.String()+"'") {
		t.Errorf("policy = %q, want the nonce the hook saw", got)
	}
}

func TestCSPNonceOutsideTheMiddleware(t *testing.T) {
	if n := middleware.CSPNonce(context.Background()); n != "" {
		t.Errorf("CSPNonce = %q, want empty", n)
	}
	r := secureRouter(middleware.SecureConfig[*appContext]{CSP: &middleware.CSPPolicy{DefaultSrc: []string{middleware.CSPSelf}}})
	r.GET("/nonce", func(c *appContext) error { return c.String(http.StatusOK, "["+middleware.CSPNonce(c)+"]") })
	if got := do(r, httptest.NewRequest(http.MethodGet, "/nonce", nil)).Body.String(); got != "[]" {
		t.Errorf("a policy without CSPWithNonce: nonce = %s, want empty", got)
	}
}

func TestSecureRefusesABadPolicy(t *testing.T) {
	self := []string{middleware.CSPSelf}
	tests := []struct {
		name string
		cfg  middleware.SecureConfig[*appContext]
		want string
	}{
		{"both policies", middleware.SecureConfig[*appContext]{ContentSecurityPolicy: "default-src 'self'", CSP: &middleware.CSPPolicy{DefaultSrc: self}}, "both ContentSecurityPolicy and CSP"},
		{"a nonce without a policy", middleware.SecureConfig[*appContext]{CSPWithNonce: true}, "CSPWithNonce without CSP"},
		{"a nonce hook without a nonce", middleware.SecureConfig[*appContext]{CSP: &middleware.CSPPolicy{DefaultSrc: self}, CSPNonceContext: func(ctx context.Context, _ string) context.Context { return ctx }}, "CSPNonceContext without CSPWithNonce"},
		{"an empty policy", middleware.SecureConfig[*appContext]{CSP: &middleware.CSPPolicy{}}, "CSP has no directive"},
		{"two sources in one", middleware.SecureConfig[*appContext]{CSP: &middleware.CSPPolicy{ScriptSrc: []string{"https://a.example https://b.example"}}}, `source "https://a.example https://b.example" in script-src`},
		{"an injected directive", middleware.SecureConfig[*appContext]{CSP: &middleware.CSPPolicy{ImgSrc: []string{"*; script-src *"}}}, "in img-src"},
		{"an empty source", middleware.SecureConfig[*appContext]{CSP: &middleware.CSPPolicy{FontSrc: []string{""}}}, `source "" in font-src`},
		{"a bare keyword", middleware.SecureConfig[*appContext]{CSP: &middleware.CSPPolicy{DefaultSrc: []string{"self"}}}, "keyword self in default-src without its quotes"},
		{"a bad report URI", middleware.SecureConfig[*appContext]{CSP: &middleware.CSPPolicy{DefaultSrc: self, ReportURI: "/a;b"}}, "in report-uri"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if msg, _ := recover().(string); !strings.Contains(msg, tt.want) {
					t.Errorf("panic = %q, want it to contain %q", msg, tt.want)
				}
			}()
			middleware.SecureWithConfig(tt.cfg)
		})
	}
}
