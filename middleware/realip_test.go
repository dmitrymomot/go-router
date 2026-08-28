package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func realIPRouter(cfg middleware.RealIPConfig) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.RealIPWithConfig[*appContext](cfg))
	r.GET("/", func(c *appContext) error {
		return c.String(http.StatusOK, middleware.ClientIP(c))
	})
	return r
}

// fromProxy names the address that made the connection, which is the proxy in
// front of the server. The default of httptest is 192.0.2.1, which no trust
// set holds.
func fromProxy(req *http.Request, addr string) *http.Request {
	req.RemoteAddr = addr
	return req
}

// forwarded builds a request that a proxy at addr made, carrying the header.
func forwarded(name, value, addr string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(name, value)
	return fromProxy(req, addr)
}

func TestRealIPTakesTheNearestUntrustedHop(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{})

	// The client forged the first entry; the proxies appended the rest. The
	// walk stops at 203.0.113.7, the nearest address it could not write.
	req := forwarded(router.HeaderXForwardedFor, "1.2.3.4, 203.0.113.7, 10.0.0.2", "10.0.0.1:9000")
	if got := do(r, req).Body.String(); got != "203.0.113.7" {
		t.Errorf("client ip = %q, want %q", got, "203.0.113.7")
	}
}

func TestRealIPIgnoresTheHeadersOfAnUntrustedPeer(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{})

	// Nothing trusted made this connection, so the header is whatever the
	// client felt like sending.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderXForwardedFor, "1.2.3.4")
	if got := do(r, req).Body.String(); got != "192.0.2.1" {
		t.Errorf("client ip = %q, want the address of the connection", got)
	}
}

func TestRealIPTakesTheLeftmostWhenEveryHopIsTrusted(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{})

	// Traffic that never left the private network: the walk runs out of
	// entries, so the first one stands.
	req := forwarded(router.HeaderXForwardedFor, "10.1.1.1, 10.0.0.2", "10.0.0.1:9000")
	if got := do(r, req).Body.String(); got != "10.1.1.1" {
		t.Errorf("client ip = %q, want %q", got, "10.1.1.1")
	}
}

func TestRealIPWithoutAHeaderKeepsTheConnection(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{})
	if got := get(r, "/").Body.String(); got != "192.0.2.1" {
		t.Errorf("client ip = %q, want %q", got, "192.0.2.1")
	}
}

func TestRealIPTrustsAConfiguredPrefix(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{
		Trust: middleware.NewTrustSet(middleware.TrustPrefix(netip.MustParsePrefix("203.0.113.0/24"))),
	})

	req := forwarded(router.HeaderXForwardedFor, "198.51.100.9, 203.0.113.6", "203.0.113.5:9000")
	if got := do(r, req).Body.String(); got != "198.51.100.9" {
		t.Errorf("client ip = %q, want %q", got, "198.51.100.9")
	}
}

func TestRealIPHonoursATrustSetThatRefusesTheRange(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{
		Trust: middleware.NewTrustSet(middleware.TrustPrivateNet(false)),
	})

	req := forwarded(router.HeaderXForwardedFor, "203.0.113.7", "10.0.0.1:9000")
	if got := do(r, req).Body.String(); got != "10.0.0.1" {
		t.Errorf("client ip = %q, want the address of the connection", got)
	}
}

func TestRealIPReadsSeveralHeaderLinesAsOneChain(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Add(router.HeaderXForwardedFor, "1.2.3.4")
	req.Header.Add(router.HeaderXForwardedFor, "203.0.113.7, 10.0.0.2")
	if got := do(r, fromProxy(req, "10.0.0.1:9000")).Body.String(); got != "203.0.113.7" {
		t.Errorf("client ip = %q, want %q", got, "203.0.113.7")
	}
}

func TestRealIPHeaderPreference(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderForwarded, "for=198.51.100.9")
	req.Header.Set(router.HeaderXForwardedFor, "203.0.113.7")
	req.Header.Set(router.HeaderXRealIP, "192.0.2.44")
	if got := do(r, fromProxy(req, "10.0.0.1:9000")).Body.String(); got != "198.51.100.9" {
		t.Errorf("client ip = %q, want the address that Forwarded names", got)
	}

	req.Header.Del(router.HeaderForwarded)
	if got := do(r, fromProxy(req, "10.0.0.1:9000")).Body.String(); got != "203.0.113.7" {
		t.Errorf("client ip = %q, want the address that X-Forwarded-For names", got)
	}

	req.Header.Del(router.HeaderXForwardedFor)
	if got := do(r, fromProxy(req, "10.0.0.1:9000")).Body.String(); got != "192.0.2.44" {
		t.Errorf("client ip = %q, want the address that X-Real-Ip names", got)
	}
}

func TestRealIPCustomHeaders(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{Headers: []string{"Cf-Connecting-Ip"}})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Cf-Connecting-Ip", "203.0.113.9")
	req.Header.Set(router.HeaderXForwardedFor, "10.0.0.1")
	if got := do(r, fromProxy(req, "10.0.0.1:9000")).Body.String(); got != "203.0.113.9" {
		t.Errorf("client ip = %q, want %q", got, "203.0.113.9")
	}
}

func TestRealIPLeftmostBringsTheOldReadingBack(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{Leftmost: true})

	// No trust set is read, so even a connection from nowhere in particular
	// hands the first entry over.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderXForwardedFor, "203.0.113.7, 10.0.0.1")
	if got := do(r, req).Body.String(); got != "203.0.113.7" {
		t.Errorf("client ip = %q, want %q", got, "203.0.113.7")
	}
}

func TestRealIPLeftmostReadsForwarded(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{Leftmost: true})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderForwarded, `for=203.0.113.7;proto=https, for=10.0.0.1`)
	if got := do(r, req).Body.String(); got != "203.0.113.7" {
		t.Errorf("client ip = %q, want %q", got, "203.0.113.7")
	}
}

func TestRealIPLeftmostPassesOverEntriesThatNameNoAddress(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{Leftmost: true})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// The Forwarded element names no address at all, so the next header in the
	// preference decides.
	req.Header.Set(router.HeaderForwarded, "by=10.0.0.2;host=app.example")
	req.Header.Add(router.HeaderXForwardedFor, ", unknown")
	req.Header.Add(router.HeaderXForwardedFor, "203.0.113.9, 10.0.0.1")
	if got := do(r, req).Body.String(); got != "203.0.113.9" {
		t.Errorf("client ip = %q, want %q", got, "203.0.113.9")
	}
}

func TestRealIPForwarded(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{
			name:   "a chain stops at the first untrusted hop",
			header: "for=198.51.100.9, for=10.0.0.2",
			want:   "198.51.100.9",
		},
		{
			name:   "an address in brackets loses them",
			header: `for="[2001:db8::1]:8080", for=10.0.0.2`,
			want:   "2001:db8::1",
		},
		{
			name:   "a quoted address without a port parses too",
			header: `for="[2001:db8::1]"`,
			want:   "2001:db8::1",
		},
		{
			name:   "the parameters may come in any order and any case",
			header: "By=10.0.0.2;PROTO=https;FOR=198.51.100.9",
			want:   "198.51.100.9",
		},
		{
			name:   "an obfuscated identifier ends the walk at the hop past it",
			header: "for=_hidden, for=10.0.0.2",
			want:   "10.0.0.2",
		},
		{
			name:   "an element without for names no address",
			header: "by=10.0.0.2;host=app.example",
			want:   "10.0.0.1",
		},
		{
			name:   "a parameter without a value is passed over",
			header: "for=198.51.100.9;secure",
			want:   "198.51.100.9",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := realIPRouter(middleware.RealIPConfig{})
			req := forwarded(router.HeaderForwarded, tt.header, "10.0.0.1:9000")
			if got := do(r, req).Body.String(); got != tt.want {
				t.Errorf("client ip = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRealIPForwardedProtoSetsTheScheme(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"the hop that stands carries the scheme", "for=198.51.100.9;proto=https", "https"},
		{"an element without for still reports the scheme", "proto=https;host=app.example", "https"},
		{"a chain reports the scheme of the hop that stands", "for=198.51.100.9;proto=https, for=10.0.0.2;proto=http", "https"},
		{"http stays http", "for=198.51.100.9;proto=http", "http"},
		{"a value that names no protocol changes nothing", "for=198.51.100.9;proto=gopher", "http"},
		{"a case of its own reads the same", "for=198.51.100.9;proto=HTTPS", "https"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRouter()
			r.Use(middleware.RealIP[*appContext])
			r.GET("/", func(c *appContext) error { return c.String(http.StatusOK, c.Scheme()) })

			req := forwarded(router.HeaderForwarded, tt.header, "10.0.0.1:9000")
			if got := do(r, req).Body.String(); got != tt.want {
				t.Errorf("scheme = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRealIPForwardedProtoLeavesTheAddressToTheNextHeader pins that a
// Forwarded header that reports the protocol alone hands the protocol on and
// still lets X-Forwarded-For name the client.
func TestRealIPForwardedProtoLeavesTheAddressToTheNextHeader(t *testing.T) {
	r := newRouter()
	r.Use(middleware.RealIP[*appContext])
	r.GET("/", func(c *appContext) error {
		return c.String(http.StatusOK, c.Scheme()+" "+middleware.ClientIP(c))
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderForwarded, "proto=https;host=app.example")
	req.Header.Set(router.HeaderXForwardedFor, "198.51.100.9, 10.0.0.2")
	if got := do(r, fromProxy(req, "10.0.0.1:9000")).Body.String(); got != "https 198.51.100.9" {
		t.Errorf("scheme and client ip = %q, want %q", got, "https 198.51.100.9")
	}
}

// TestRealIPForwardedProtoReplacesTheOlderHeader pins that the header the
// middleware read wins over the one it did not, so a client cannot claim HTTPS
// past a proxy that reported plain HTTP.
func TestRealIPForwardedProtoReplacesTheOlderHeader(t *testing.T) {
	r := newRouter()
	r.Use(middleware.RealIP[*appContext])
	r.GET("/", func(c *appContext) error { return c.String(http.StatusOK, c.Scheme()) })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderForwarded, "for=198.51.100.9;proto=http")
	req.Header.Set(router.HeaderXForwardedProto, "https")
	if got := do(r, fromProxy(req, "10.0.0.1:9000")).Body.String(); got != "http" {
		t.Errorf("scheme = %q, want %q", got, "http")
	}
}

// TestRealIPLeavesTheRequestThatCameInAlone pins that the header map of the
// incoming request is not written through, because the shallow copy shares it.
func TestRealIPLeavesTheRequestThatCameInAlone(t *testing.T) {
	var incoming *http.Request

	r := newRouter()
	r.Use(func(next router.HandlerFunc[*appContext]) router.HandlerFunc[*appContext] {
		return func(c *appContext) error {
			incoming = c.Request()
			return next(c)
		}
	})
	r.Use(middleware.RealIP[*appContext])
	r.GET("/", func(c *appContext) error { return c.String(http.StatusOK, c.Scheme()) })

	req := forwarded(router.HeaderForwarded, "for=198.51.100.9;proto=https", "10.0.0.1:9000")
	if got := do(r, req).Body.String(); got != "https" {
		t.Fatalf("scheme = %q, want %q", got, "https")
	}
	if got := incoming.Header.Get(router.HeaderXForwardedProto); got != "" {
		t.Errorf("the request that came in gained %s: %q", router.HeaderXForwardedProto, got)
	}
	if incoming.RemoteAddr != "10.0.0.1:9000" {
		t.Errorf("the request that came in gained a new address: %q", incoming.RemoteAddr)
	}
}

func TestRealIPSkip(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{Skip: skipPath("/")})

	req := forwarded(router.HeaderXForwardedFor, "203.0.113.7", "10.0.0.1:9000")
	if got := do(r, req).Body.String(); got != "10.0.0.1" {
		t.Errorf("client ip = %q, want the connection address", got)
	}
}
