package middleware_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
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

// namedRealIP is the middleware of a deployment whose proxies write the named
// headers and write nothing else.
func namedRealIP(names ...string) router.Middleware[*appContext] {
	return middleware.RealIPWithConfig[*appContext](middleware.RealIPConfig{Headers: names})
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
	r := realIPRouter(middleware.RealIPConfig{Headers: []string{router.HeaderXForwardedFor}})

	// The client forged the first entry; the proxies appended the rest. The
	// walk stops at 203.0.113.7, the nearest address it could not write.
	req := forwarded(router.HeaderXForwardedFor, "1.2.3.4, 203.0.113.7, 10.0.0.2", "10.0.0.1:9000")
	if got := do(r, req).Body.String(); got != "203.0.113.7" {
		t.Errorf("client ip = %q, want %q", got, "203.0.113.7")
	}
}

func TestRealIPIgnoresTheHeadersOfAnUntrustedPeer(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{Headers: []string{router.HeaderXForwardedFor}})

	// Nothing trusted made this connection, so the header is whatever the
	// client felt like sending.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderXForwardedFor, "1.2.3.4")
	if got := do(r, req).Body.String(); got != "192.0.2.1" {
		t.Errorf("client ip = %q, want the address of the connection", got)
	}
}

func TestRealIPTakesTheLeftmostWhenEveryHopIsTrusted(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{Headers: []string{router.HeaderXForwardedFor}})

	// Traffic that never left the private network: the walk runs out of
	// entries, so the first one stands.
	req := forwarded(router.HeaderXForwardedFor, "10.1.1.1, 10.0.0.2", "10.0.0.1:9000")
	if got := do(r, req).Body.String(); got != "10.1.1.1" {
		t.Errorf("client ip = %q, want %q", got, "10.1.1.1")
	}
}

func TestRealIPWithoutAHeaderKeepsTheConnection(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{Headers: []string{router.HeaderXForwardedFor}})
	if got := get(r, "/").Body.String(); got != "192.0.2.1" {
		t.Errorf("client ip = %q, want %q", got, "192.0.2.1")
	}
}

func TestRealIPTrustsAConfiguredPrefix(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{
		Headers: []string{router.HeaderXForwardedFor},
		Trust:   middleware.NewTrustSet(middleware.TrustPrefix(netip.MustParsePrefix("203.0.113.0/24"))),
	})

	req := forwarded(router.HeaderXForwardedFor, "198.51.100.9, 203.0.113.6", "203.0.113.5:9000")
	if got := do(r, req).Body.String(); got != "198.51.100.9" {
		t.Errorf("client ip = %q, want %q", got, "198.51.100.9")
	}
}

func TestRealIPHonoursATrustSetThatRefusesTheRange(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{
		Headers: []string{router.HeaderXForwardedFor},
		Trust:   middleware.NewTrustSet(middleware.TrustPrivateNet(false)),
	})

	req := forwarded(router.HeaderXForwardedFor, "203.0.113.7", "10.0.0.1:9000")
	if got := do(r, req).Body.String(); got != "10.0.0.1" {
		t.Errorf("client ip = %q, want the address of the connection", got)
	}
}

func TestRealIPReadsSeveralHeaderLinesAsOneChain(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{Headers: []string{router.HeaderXForwardedFor}})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Add(router.HeaderXForwardedFor, "1.2.3.4")
	req.Header.Add(router.HeaderXForwardedFor, "203.0.113.7, 10.0.0.2")
	if got := do(r, fromProxy(req, "10.0.0.1:9000")).Body.String(); got != "203.0.113.7" {
		t.Errorf("client ip = %q, want %q", got, "203.0.113.7")
	}
}

// TestRealIPHeaderPreference pins the order of a list that names more than one
// header, which is what a deployment whose proxy writes all three declares.
func TestRealIPHeaderPreference(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{Headers: []string{
		router.HeaderForwarded, router.HeaderXForwardedFor, router.HeaderXRealIP,
	}})

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
	r := realIPRouter(middleware.RealIPConfig{
		Headers:  []string{router.HeaderXForwardedFor},
		Leftmost: true,
	})

	// No trust set is read, so even a connection from nowhere in particular
	// hands the first entry over.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderXForwardedFor, "203.0.113.7, 10.0.0.1")
	if got := do(r, req).Body.String(); got != "203.0.113.7" {
		t.Errorf("client ip = %q, want %q", got, "203.0.113.7")
	}
}

func TestRealIPLeftmostReadsForwarded(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{
		Headers:  []string{router.HeaderForwarded},
		Leftmost: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderForwarded, `for=203.0.113.7;proto=https, for=10.0.0.1`)
	if got := do(r, req).Body.String(); got != "203.0.113.7" {
		t.Errorf("client ip = %q, want %q", got, "203.0.113.7")
	}
}

func TestRealIPLeftmostPassesOverEntriesThatNameNoAddress(t *testing.T) {
	r := realIPRouter(middleware.RealIPConfig{
		Headers:  []string{router.HeaderForwarded, router.HeaderXForwardedFor},
		Leftmost: true,
	})

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
			r := realIPRouter(middleware.RealIPConfig{Headers: []string{router.HeaderForwarded}})
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
			r.Use(namedRealIP(router.HeaderForwarded))
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
	r.Use(namedRealIP(router.HeaderForwarded, router.HeaderXForwardedFor))
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
	r.Use(namedRealIP(router.HeaderForwarded))
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
	r.Use(namedRealIP(router.HeaderForwarded))
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
	r := realIPRouter(middleware.RealIPConfig{
		Headers: []string{router.HeaderXForwardedFor},
		Skip:    skipPath("/"),
	})

	req := forwarded(router.HeaderXForwardedFor, "203.0.113.7", "10.0.0.1:9000")
	if got := do(r, req).Body.String(); got != "10.0.0.1" {
		t.Errorf("client ip = %q, want the connection address", got)
	}
}

// headerEchoRouter answers with the client address, the scheme, and whatever
// the request still carries in the named header, which is what shows which
// headers reached the handler.
func headerEchoRouter(cfg middleware.RealIPConfig, name string) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.RealIPWithConfig[*appContext](cfg))
	r.GET("/", func(c *appContext) error {
		return c.String(http.StatusOK, middleware.ClientIP(c)+" "+c.Scheme()+" "+
			strings.Join(c.Request().Header.Values(name), "|"))
	})
	return r
}

// TestRealIPReadsOnlyTheHeadersItNames pins that a header the deployment's
// proxy does not write cannot outrank the one it does. The proxy appends to
// one header and relays the rest untouched, so the rest is whatever the client
// felt like sending.
func TestRealIPReadsOnlyTheHeadersItNames(t *testing.T) {
	tests := []struct {
		name    string
		named   string
		written string
		forged  map[string]string
	}{
		{
			name:    "the proxy writes X-Forwarded-For",
			named:   router.HeaderXForwardedFor,
			written: "198.51.100.5, 10.0.0.2",
			forged: map[string]string{
				router.HeaderForwarded: "for=1.2.3.4;proto=https",
				router.HeaderXRealIP:   "1.2.3.4",
			},
		},
		{
			name:    "the proxy writes X-Real-Ip",
			named:   router.HeaderXRealIP,
			written: "198.51.100.5",
			forged: map[string]string{
				router.HeaderForwarded:     "for=1.2.3.4;proto=https",
				router.HeaderXForwardedFor: "1.2.3.4",
			},
		},
		{
			name:    "the proxy writes Forwarded",
			named:   router.HeaderForwarded,
			written: "for=198.51.100.5;proto=http, for=10.0.0.2",
			forged: map[string]string{
				router.HeaderXForwardedFor: "1.2.3.4",
				router.HeaderXRealIP:       "1.2.3.4",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := headerEchoRouter(middleware.RealIPConfig{Headers: []string{tt.named}}, tt.named)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(tt.named, tt.written)
			for name, value := range tt.forged {
				req.Header.Set(name, value)
			}
			want := "198.51.100.5 http " + tt.written
			if got := do(r, fromProxy(req, "10.0.0.1:9000")).Body.String(); got != want {
				t.Errorf("client ip, scheme and header = %q, want %q", got, want)
			}
		})
	}
}

// TestRealIPDeletesTheHeadersItDoesNotRead pins that a relayed header reaches
// neither a later middleware nor the handler, so nothing downstream reads an
// address that no proxy of this deployment wrote.
func TestRealIPDeletesTheHeadersItDoesNotRead(t *testing.T) {
	for _, name := range []string{router.HeaderForwarded, router.HeaderXRealIP} {
		t.Run(name, func(t *testing.T) {
			cfg := middleware.RealIPConfig{Headers: []string{router.HeaderXForwardedFor}}
			r := headerEchoRouter(cfg, name)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(router.HeaderXForwardedFor, "198.51.100.5")
			req.Header.Set(name, "for=1.2.3.4")
			if got := do(r, fromProxy(req, "10.0.0.1:9000")).Body.String(); got != "198.51.100.5 http " {
				t.Errorf("client ip, scheme and %s = %q, want the header gone", name, got)
			}
		})
	}
}

// TestRealIPDefaultReadsNoHeader pins that a config naming no header reads
// none. Nothing says which header the proxy of this deployment writes, so
// every one of them is a header the client writes.
func TestRealIPDefaultReadsNoHeader(t *testing.T) {
	r := headerEchoRouter(middleware.RealIPConfig{}, router.HeaderXForwardedFor)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderXForwardedFor, "1.2.3.4")
	req.Header.Set(router.HeaderForwarded, "for=1.2.3.4;proto=https")
	if got := do(r, fromProxy(req, "10.0.0.1:9000")).Body.String(); got != "10.0.0.1 http " {
		t.Errorf("client ip, scheme and %s = %q, want the connection address and no header",
			router.HeaderXForwardedFor, got)
	}
}

// TestRealIPDeletesTheHeadersOfAnUntrustedPeer pins that the headers of a
// connection no trusted proxy made go unread by everything after this
// middleware too, not by this middleware alone.
func TestRealIPDeletesTheHeadersOfAnUntrustedPeer(t *testing.T) {
	cfg := middleware.RealIPConfig{Headers: []string{router.HeaderXForwardedFor}}
	r := headerEchoRouter(cfg, router.HeaderXForwardedFor)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderXForwardedFor, "1.2.3.4")
	if got := do(r, req).Body.String(); got != "192.0.2.1 http " {
		t.Errorf("client ip, scheme and header = %q, want the connection address and no header", got)
	}
}

// TestRealIPKeepsTheSchemeOfTheProxy pins that a client cannot talk the server
// into believing the hop was TLS. The proxy of this deployment writes
// X-Forwarded-For and X-Forwarded-Proto; the Forwarded header that the client
// sent alongside them is read by nothing and reaches nothing, so the plaintext
// hop that the proxy reported stands.
func TestRealIPKeepsTheSchemeOfTheProxy(t *testing.T) {
	cfg := middleware.RealIPConfig{Headers: []string{router.HeaderXForwardedFor}}
	r := headerEchoRouter(cfg, router.HeaderForwarded)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderXForwardedFor, "198.51.100.5")
	req.Header.Set(router.HeaderXForwardedProto, "http")
	req.Header.Set(router.HeaderForwarded, "for=1.2.3.4;proto=https")
	if got := do(r, fromProxy(req, "10.0.0.1:9000")).Body.String(); got != "198.51.100.5 http " {
		t.Errorf("client ip, scheme and %s = %q, want %q",
			router.HeaderForwarded, got, "198.51.100.5 http ")
	}
}

// TestRealIPRemoteAddrHoldsABareAddress pins the shape that the middleware
// writes into RemoteAddr: a hop that named no port yields no port, so
// [net.SplitHostPort] does not read it and [middleware.ClientIP] does.
func TestRealIPRemoteAddrHoldsABareAddress(t *testing.T) {
	tests := []struct {
		name  string
		hop   string
		addr  string
		ip    string
		split bool
	}{
		{"an address alone", "203.0.113.7", "203.0.113.7", "203.0.113.7", false},
		{"an address with a port", "203.0.113.7:1234", "203.0.113.7:1234", "203.0.113.7", true},
		{"an IPv6 address alone", "2001:db8::1", "2001:db8::1", "2001:db8::1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRouter()
			r.Use(namedRealIP(router.HeaderXForwardedFor))
			r.GET("/", func(c *appContext) error {
				_, _, err := net.SplitHostPort(c.Request().RemoteAddr)
				return c.String(http.StatusOK, c.Request().RemoteAddr+" "+
					middleware.ClientIP(c)+" "+strconv.FormatBool(err == nil))
			})

			req := forwarded(router.HeaderXForwardedFor, tt.hop, "10.0.0.1:9000")
			want := tt.addr + " " + tt.ip + " " + strconv.FormatBool(tt.split)
			if got := do(r, req).Body.String(); got != want {
				t.Errorf("remote addr, client ip and split = %q, want %q", got, want)
			}
		})
	}
}

// TestRealIPDeletesTheForwardedProtoOfAnUntrustedPeer pins that a client which
// reached the server itself cannot name the scheme of its own request. No
// proxy made this connection, so no proxy wrote the header, and the scheme of
// the request is the plaintext one that arrived.
//
// It matters past this middleware: [router.Base.Scheme] is what marks a cookie
// Secure and what builds an absolute URL, so a client that picks the scheme
// picks both.
func TestRealIPDeletesTheForwardedProtoOfAnUntrustedPeer(t *testing.T) {
	tests := []struct {
		name string
		cfg  middleware.RealIPConfig
	}{
		{"no header named", middleware.RealIPConfig{}},
		{"the proxy writes X-Forwarded-For", middleware.RealIPConfig{
			Headers: []string{router.HeaderXForwardedFor},
		}},
		{"the proxy writes Forwarded", middleware.RealIPConfig{
			Headers: []string{router.HeaderForwarded},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := headerEchoRouter(tt.cfg, router.HeaderXForwardedProto)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(router.HeaderXForwardedProto, "https")
			// 192.0.2.1, the default of httptest, is in no trust set.
			if got := do(r, req).Body.String(); got != "192.0.2.1 http " {
				t.Errorf("client ip, scheme and %s = %q, want %q",
					router.HeaderXForwardedProto, got, "192.0.2.1 http ")
			}
		})
	}
}

// TestRealIPKeepsTheForwardedProtoOfATrustedProxy pins the other side: the
// proxy of the deployment writes X-Forwarded-Proto itself, this middleware
// reads and writes no other one, and deleting it would tell every deployment
// that terminates TLS at a proxy that its requests arrived in plaintext.
func TestRealIPKeepsTheForwardedProtoOfATrustedProxy(t *testing.T) {
	cfg := middleware.RealIPConfig{Headers: []string{router.HeaderXForwardedFor}}
	r := headerEchoRouter(cfg, router.HeaderXForwardedProto)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderXForwardedFor, "198.51.100.5")
	req.Header.Set(router.HeaderXForwardedProto, "https")
	if got := do(r, fromProxy(req, "10.0.0.1:9000")).Body.String(); got != "198.51.100.5 https https" {
		t.Errorf("client ip, scheme and %s = %q, want %q",
			router.HeaderXForwardedProto, got, "198.51.100.5 https https")
	}
}
