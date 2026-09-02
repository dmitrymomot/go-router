package middleware_test

import (
	"net/netip"
	"testing"

	"github.com/dmitrymomot/go-router/middleware"
)

func TestTrustSetDefaults(t *testing.T) {
	set := middleware.NewTrustSet()
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.10.0.1", true},
		{"::1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"fd00::1", true},
		{"169.254.1.1", true},
		{"fe80::1", true},
		{"203.0.113.7", false},
		{"2001:db8::1", false},
		{"8.8.8.8", false},
		{"::ffff:10.0.0.1", true},
		{"::ffff:203.0.113.7", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			if got := set.Trusted(netip.MustParseAddr(tt.addr)); got != tt.want {
				t.Errorf("Trusted(%s) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestTrustSetOptionsTurnARangeOff(t *testing.T) {
	tests := []struct {
		name string
		opt  middleware.TrustOption
		addr string
	}{
		{"loopback", middleware.TrustLoopback(false), "127.0.0.1"},
		{"private", middleware.TrustPrivateNet(false), "10.0.0.1"},
		{"link local", middleware.TrustLinkLocal(false), "169.254.1.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := netip.MustParseAddr(tt.addr)
			if !middleware.NewTrustSet().Trusted(addr) {
				t.Fatalf("Trusted(%s) = false by default, so the option proves nothing", tt.addr)
			}
			if middleware.NewTrustSet(tt.opt).Trusted(addr) {
				t.Errorf("Trusted(%s) = true, want false once the option turned the range off", tt.addr)
			}
		})
	}
}

func TestTrustSetPrefixAddsARange(t *testing.T) {
	set := middleware.NewTrustSet(
		middleware.TrustLoopback(false),
		middleware.TrustPrivateNet(false),
		middleware.TrustLinkLocal(false),
		middleware.TrustPrefix(netip.MustParsePrefix("203.0.113.0/24")),
		middleware.TrustPrefix(netip.MustParsePrefix("2001:db8::/32")),
	)
	tests := []struct {
		addr string
		want bool
	}{
		{"203.0.113.0", true},
		{"203.0.113.255", true},
		{"203.0.114.1", false},
		{"2001:db8::1", true},
		{"2001:db9::1", false},
		{"10.0.0.1", false},
		{"127.0.0.1", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			if got := set.Trusted(netip.MustParseAddr(tt.addr)); got != tt.want {
				t.Errorf("Trusted(%s) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestTrustSetPrefixTakesAnUnmaskedPrefix(t *testing.T) {
	set := middleware.NewTrustSet(middleware.TrustPrefix(netip.MustParsePrefix("203.0.113.42/24")))
	if !set.Trusted(netip.MustParseAddr("203.0.113.7")) {
		t.Error("Trusted(203.0.113.7) = false, want true")
	}
}

func TestTrustSetIgnoresAZone(t *testing.T) {
	set := middleware.NewTrustSet(
		middleware.TrustLinkLocal(false),
		middleware.TrustPrefix(netip.MustParsePrefix("fe80::/10")),
	)
	addr := netip.MustParseAddr("fe80::1%eth0")
	if !set.Trusted(addr) {
		t.Errorf("Trusted(%s) = false, want true", addr)
	}
}

func TestTrustSetTrustsNothingWithoutOne(t *testing.T) {
	addr := netip.MustParseAddr("127.0.0.1")

	var zero middleware.TrustSet
	if zero.Trusted(addr) {
		t.Error("the zero value trusted the loopback")
	}

	var nilSet *middleware.TrustSet
	if nilSet.Trusted(addr) {
		t.Error("a nil set trusted the loopback")
	}
}

func TestTrustSetRefusesAnInvalidAddress(t *testing.T) {
	if middleware.NewTrustSet().Trusted(netip.Addr{}) {
		t.Error("Trusted(invalid) = true, want false")
	}
}

func TestNewTrustSetRejectsANilOption(t *testing.T) {
	mustPanicContaining(t, "nil option", func() {
		middleware.NewTrustSet(nil)
	})
}

// A zero Prefix matches nothing, so the proxy is never trusted and the
// misconfiguration looks like working code.
func TestTrustPrefixRejectsAnInvalidPrefix(t *testing.T) {
	bad, err := netip.ParsePrefix("not-a-prefix")
	if err == nil {
		t.Fatal("ParsePrefix accepted nonsense")
	}
	defer func() {
		if recover() == nil {
			t.Error("TrustPrefix accepted an invalid prefix")
		}
	}()
	middleware.TrustPrefix(bad)
}
