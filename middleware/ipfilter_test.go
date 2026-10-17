package middleware_test

import (
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
	"github.com/dmitrymomot/go-router/routertest"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func filtered(cfg middleware.IPFilterConfig[*Context], mws ...router.Middleware[*Context]) *router.Router[*Context] {
	r := newAPI()
	r.Use(mws...)
	r.Use(middleware.IPFilterWithConfig(cfg))
	r.GET("/", sayOK)
	r.GET("/healthz", sayOK)
	return r
}

func statusFrom(r http.Handler, remote string, opts ...routertest.RequestOption) int {
	return routertest.Get(r, "/", append(opts, routertest.RemoteAddr(remote))...).StatusCode
}

func TestIPFilter(t *testing.T) {
	tests := []struct {
		name   string
		cfg    middleware.IPFilterConfig[*Context]
		remote string
		want   int
	}{
		{"allowlist admits a member", middleware.IPFilterConfig[*Context]{Allow: []netip.Prefix{pfx("203.0.113.0/24")}}, "203.0.113.10:1", http.StatusOK},
		{"allowlist refuses a stranger", middleware.IPFilterConfig[*Context]{Allow: []netip.Prefix{pfx("203.0.113.0/24")}}, "198.51.100.1:1", http.StatusForbidden},
		{"denylist refuses a member", middleware.IPFilterConfig[*Context]{Deny: []netip.Prefix{pfx("198.51.100.0/24")}}, "198.51.100.1:1", http.StatusForbidden},
		{"denylist admits a stranger", middleware.IPFilterConfig[*Context]{Deny: []netip.Prefix{pfx("198.51.100.0/24")}}, "203.0.113.10:1", http.StatusOK},
		{"deny wins over allow", middleware.IPFilterConfig[*Context]{Allow: []netip.Prefix{pfx("203.0.113.0/24")}, Deny: []netip.Prefix{pfx("203.0.113.66/32")}}, "203.0.113.66:1", http.StatusForbidden},
		{"the rest of the allowed range", middleware.IPFilterConfig[*Context]{Allow: []netip.Prefix{pfx("203.0.113.0/24")}, Deny: []netip.Prefix{pfx("203.0.113.66/32")}}, "203.0.113.67:1", http.StatusOK},
		{"IPv6 deny", middleware.IPFilterConfig[*Context]{Deny: []netip.Prefix{pfx("2001:db8::/32")}}, "[2001:db8::1]:443", http.StatusForbidden},
		{"a mapped peer against an IPv4 range", middleware.IPFilterConfig[*Context]{Allow: []netip.Prefix{pfx("10.0.0.0/8")}}, "[::ffff:10.1.2.3]:1", http.StatusOK},
		{"a mapped prefix", middleware.IPFilterConfig[*Context]{Allow: []netip.Prefix{pfx("::ffff:10.0.0.0/104")}}, "10.1.2.3:1", http.StatusOK},
		{"a zoned peer", middleware.IPFilterConfig[*Context]{Allow: []netip.Prefix{pfx("fe80::/10")}}, "[fe80::1%en0]:1", http.StatusOK},
		{"an allowlist refuses a peer with no address", middleware.IPFilterConfig[*Context]{Allow: []netip.Prefix{pfx("0.0.0.0/0")}}, "@", http.StatusForbidden},
		{"a denylist admits a peer with no address", middleware.IPFilterConfig[*Context]{Deny: []netip.Prefix{pfx("0.0.0.0/0")}}, "@", http.StatusOK},
		{"host bits are masked", middleware.IPFilterConfig[*Context]{Allow: []netip.Prefix{pfx("203.0.113.77/24")}}, "203.0.113.1:1", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusFrom(filtered(tt.cfg), tt.remote); got != tt.want {
				t.Errorf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestIPFilterReadsTheClientBehindRealIP(t *testing.T) {
	r := filtered(middleware.IPFilterConfig[*Context]{Allow: []netip.Prefix{pfx("203.0.113.0/24")}},
		middleware.RealIPWithConfig(middleware.RealIPConfig[*Context]{
			Trust:   middleware.NewTrustSet(),
			Headers: []string{router.HeaderXForwardedFor},
		}))
	xff := func(v string) routertest.RequestOption { return routertest.Header(router.HeaderXForwardedFor, v) }
	if got := statusFrom(r, "10.0.0.1:1", xff("203.0.113.5")); got != http.StatusOK {
		t.Errorf("a client in range behind the proxy: %d, want 200", got)
	}
	if got := statusFrom(r, "10.0.0.1:1", xff("198.51.100.5")); got != http.StatusForbidden {
		t.Errorf("a client out of range behind the proxy: %d, want 403", got)
	}
	// An untrusted peer cannot claim an address in range.
	if got := statusFrom(r, "198.51.100.9:1", xff("203.0.113.5")); got != http.StatusForbidden {
		t.Errorf("a forged header: %d, want 403", got)
	}
}

func TestIPFilterOnRejectAndSkip(t *testing.T) {
	var got error
	r := filtered(middleware.IPFilterConfig[*Context]{
		Allow: []netip.Prefix{pfx("203.0.113.0/24")},
		Skip:  func(c *Context) bool { return c.Path() == "/healthz" },
		OnReject: func(c *Context, err error) error {
			got = err
			return c.String(http.StatusNotFound, "nothing here")
		},
	})
	routertest.Get(r, "/", routertest.RemoteAddr("198.51.100.1:1")).Expect(t).Status(http.StatusNotFound).Body("nothing here")
	if !errors.Is(got, router.ErrForbidden) || !strings.Contains(got.Error(), "198.51.100.1 is not allowed") {
		t.Errorf("err = %v, want a 403 that names the client", got)
	}
	routertest.Get(r, "/healthz", routertest.RemoteAddr("198.51.100.1:1")).Expect(t).Status(http.StatusOK)
	r = filtered(middleware.IPFilterConfig[*Context]{Allow: []netip.Prefix{pfx("10.0.0.0/8")}, OnReject: func(_ *Context, err error) error {
		got = err
		return err
	}})
	routertest.Get(r, "/", routertest.RemoteAddr("@")).Expect(t).Status(http.StatusForbidden)
	if !strings.Contains(got.Error(), `the peer "@" has no IP address`) {
		t.Errorf("err = %v, want the peer named", got)
	}
}

func TestIPFilterShortForm(t *testing.T) {
	r := newAPI()
	r.Use(middleware.IPFilter[*Context](pfx("192.0.2.0/24")))
	r.GET("/", sayOK)
	if got := statusFrom(r, "192.0.2.1:1"); got != http.StatusOK {
		t.Errorf("status = %d, want 200", got)
	}
}

func TestIPFilterRefusesABadConfig(t *testing.T) {
	tests := []struct {
		name string
		f    func()
		want string
	}{
		{"no list", func() { middleware.IPFilter[*Context]() }, "needs an Allow or a Deny prefix"},
		{"zero allow", func() { middleware.IPFilter[*Context](netip.Prefix{}) }, "IPFilterConfig.Allow[0] is not a valid prefix"},
		{"zero deny", func() {
			middleware.IPFilterWithConfig(middleware.IPFilterConfig[*Context]{Deny: []netip.Prefix{pfx("10.0.0.0/8"), {}}})
		}, "IPFilterConfig.Deny[1] is not a valid prefix"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if msg, _ := recover().(string); !strings.Contains(msg, tt.want) {
					t.Errorf("panic = %q, want it to contain %q", msg, tt.want)
				}
			}()
			tt.f()
		})
	}
}
