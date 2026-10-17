package middleware_test

import (
	"net/http"
	"net/netip"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
	"github.com/dmitrymomot/go-router/routertest"
)

// clientOf answers with the address that c.ClientIP reports behind cfg.
func clientOf(t *testing.T, cfg middleware.RealIPConfig[*Context], remote string, header ...string) string {
	t.Helper()
	r := newAPI()
	r.Use(middleware.RealIPWithConfig(cfg))
	r.GET("/", func(c *Context) error { return c.String(http.StatusOK, c.ClientIP()) })
	opts := []routertest.RequestOption{routertest.RemoteAddr(remote)}
	for i := 0; i+1 < len(header); i += 2 {
		opts = append(opts, routertest.Header(header[i], header[i+1]))
	}
	return routertest.Get(r, "/", opts...).String()
}

func TestRealIPEdgePresets(t *testing.T) {
	trustEdge := middleware.NewTrustSet(
		middleware.TrustPrivateNet(false),
		middleware.TrustCloudflare(),
		middleware.TrustFastly(),
		middleware.TrustPrefix(netip.MustParsePrefix("130.176.0.0/16")),
	)
	tests := []struct {
		name, header, value, remote, want string
	}{
		{"Cloudflare", middleware.HeaderCFConnectingIP, "198.51.100.7", "173.245.48.10:443", "198.51.100.7"},
		{"Cloudflare over IPv6", middleware.HeaderCFConnectingIP, "2001:db8::7", "[2606:4700::1]:443", "2001:db8::7"},
		{"Cloudflare from a stranger", middleware.HeaderCFConnectingIP, "198.51.100.7", "203.0.113.1:443", "203.0.113.1"},
		{"Fastly", middleware.HeaderFastlyClientIP, "198.51.100.8", "151.101.1.1:443", "198.51.100.8"},
		{"Fastly over IPv6", middleware.HeaderFastlyClientIP, "198.51.100.8", "[2a04:4e42::9]:443", "198.51.100.8"},
		{"CloudFront drops the port", middleware.HeaderCloudFrontViewerAddress, "203.0.113.8:52193", "130.176.1.1:443", "203.0.113.8"},
		{"CloudFront drops the port of IPv6", middleware.HeaderCloudFrontViewerAddress, "2001:db8:3c4d:15::d234:3eb5:46532", "130.176.1.1:443", "2001:db8:3c4d:15::d234:3eb5"},
		{"CloudFront in brackets", middleware.HeaderCloudFrontViewerAddress, "[2001:db8::8]:52193", "130.176.1.1:443", "2001:db8::8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := middleware.RealIPConfig[*Context]{Trust: trustEdge, Headers: []string{tt.header}}
			if got := clientOf(t, cfg, tt.remote, tt.header, tt.value); got != tt.want {
				t.Errorf("client = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRealIPEdgeHeadersOfAProxyOnThePrivateNetwork(t *testing.T) {
	for _, header := range []string{middleware.HeaderTrueClientIP, middleware.HeaderXAzureClientIP, middleware.HeaderXEnvoyExternalAddress} {
		cfg := middleware.RealIPConfig[*Context]{Trust: middleware.NewTrustSet(), Headers: []string{header}}
		if got := clientOf(t, cfg, "10.0.0.1:1", header, "203.0.113.4"); got != "203.0.113.4" {
			t.Errorf("%s: client = %q, want 203.0.113.4", header, got)
		}
		if got := clientOf(t, cfg, "10.0.0.1:1"); got != "10.0.0.1" {
			t.Errorf("%s absent: client = %q, want the peer", header, got)
		}
	}
}

func TestRealIPSkipPrivate(t *testing.T) {
	cfg := middleware.RealIPConfig[*Context]{Headers: []string{router.HeaderXForwardedFor}, Leftmost: true, SkipPrivate: true}
	tests := []struct{ chain, want string }{
		{"10.9.9.9, 203.0.113.5, 10.0.0.2", "203.0.113.5"},
		{"100.64.1.1, 127.0.0.1, fe80::1, fd00::1, 2001:db8::5", "2001:db8::5"},
		{"::ffff:10.1.1.1, ::ffff:198.51.100.3", "::ffff:198.51.100.3"},
		{"10.9.9.9, 192.168.1.1", "10.0.0.2"},
		{"garbage, 0.0.0.0", "10.0.0.2"},
	}
	for _, tt := range tests {
		if got := clientOf(t, cfg, "10.0.0.2:80", router.HeaderXForwardedFor, tt.chain); got != tt.want {
			t.Errorf("chain %q: client = %q, want %q", tt.chain, got, tt.want)
		}
	}
	plain := middleware.RealIPConfig[*Context]{Headers: []string{router.HeaderXForwardedFor}, Leftmost: true}
	if got := clientOf(t, plain, "10.0.0.2:80", router.HeaderXForwardedFor, "10.9.9.9, 203.0.113.5"); got != "10.9.9.9" {
		t.Errorf("Leftmost alone: client = %q, want the first entry", got)
	}
}

func TestRealIPSkipPrivateNeedsLeftmost(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("SkipPrivate without Leftmost did not panic")
		}
	}()
	middleware.RealIPWithConfig(middleware.RealIPConfig[*Context]{Headers: []string{router.HeaderXForwardedFor}, SkipPrivate: true})
}

func TestTrustSharedNet(t *testing.T) {
	addr := netip.MustParseAddr("100.64.3.4")
	if middleware.NewTrustSet().Trusted(addr) {
		t.Error("the default set trusts the shared address space")
	}
	if !middleware.NewTrustSet(middleware.TrustSharedNet(true)).Trusted(addr) {
		t.Error("TrustSharedNet(true) does not trust 100.64.3.4")
	}
}
