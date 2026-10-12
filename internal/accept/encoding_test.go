package accept

import "testing"

func TestQuality(t *testing.T) {
	tests := []struct {
		params string
		want   float64
	}{
		{"", 1},
		{";charset=utf-8", 1},
		{";q=0", 0},
		{"; q = 0.5 ", 0.5},
		{";Q=0.123", 0.123},
		{";q=1", 1},
		{";q=1.000", 1},
		{";q=1.", 1},
		{";q=0.", 0},
		{";q=0.1234", 0},
		{";q=1.5", 0},
		{";q=1.001", 0},
		{";q=2", 0},
		{";q=-0.5", 0},
		{";q=+0.5", 0},
		{";q=.5", 0},
		{";q=1e0", 0},
		{";q=0x1", 0},
		{";q=NaN", 0},
		{";q=Inf", 0},
		{";q=abc", 0},
		{";q=", 0},
		{";level=1;q=0.4", 0.4},
	}
	for _, tc := range tests {
		if got := Quality(tc.params); got != tc.want {
			t.Errorf("Quality(%q) = %v; want %v", tc.params, got, tc.want)
		}
	}
}

func TestEncodingQuality(t *testing.T) {
	tests := []struct {
		header, coding string
		want           float64
	}{
		{"gzip", "gzip", 1},
		{"GZip;q=0.5", "gzip", 0.5},
		{" gzip ; q=0.5 , br", "br", 1},
		{"x-gzip", "gzip", 1},
		{"gzip", "br", 0},
		{"", "gzip", 0},
		{"*", "br", 1},
		{"*;q=0.3", "br", 0.3},
		{"gzip;q=0, *", "gzip", 0},
		{"gzip;q=0, *", "br", 1},
		{"*, gzip;q=0.2", "gzip", 0.2},
		{"gzip;q=0.9, gzip;q=0", "gzip", 0.9},
		{"gzip;q=abc, *", "gzip", 0},
		{"gzip;q=2", "gzip", 0},
		{"gzip;q=0.0001", "gzip", 0},
		{"", "identity", 1},
		{"gzip", "identity", 1},
		{"identity;q=0", "identity", 0},
		{"*;q=0", "identity", 0},
		{"*;q=0, identity", "identity", 1},
		{"*;q=0, identity;q=0.5", "identity", 0.5},
		{"gzip, *;q=0", "identity", 0},
	}
	for _, tc := range tests {
		if got := EncodingQuality(tc.header, tc.coding); got != tc.want {
			t.Errorf("EncodingQuality(%q, %q) = %v; want %v", tc.header, tc.coding, got, tc.want)
		}
	}
}

func TestNegotiateEncoding(t *testing.T) {
	tests := []struct {
		header string
		offers []string
		want   string
	}{
		{"gzip, br", []string{"br", "gzip"}, "br"},
		{"gzip, br", []string{"gzip", "br"}, "gzip"},
		{"gzip;q=0.9, br;q=0.5", []string{"br", "gzip"}, "gzip"},
		{"*", []string{"br", "gzip"}, "br"},
		{"*;q=0, gzip", []string{"br", "gzip"}, "gzip"},
		{"br;q=0, *", []string{"br", "gzip"}, "gzip"},
		{"identity", []string{"br", "gzip"}, ""},
		{"", []string{"gzip"}, ""},
		{"gzip", nil, ""},
	}
	for _, tc := range tests {
		if got := NegotiateEncoding(tc.header, tc.offers...); got != tc.want {
			t.Errorf("NegotiateEncoding(%q, %v) = %q; want %q", tc.header, tc.offers, got, tc.want)
		}
	}
}
