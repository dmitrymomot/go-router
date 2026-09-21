package middleware

import (
	"testing"
)

func FuzzAcceptsGzip(f *testing.F) {
	for _, seed := range []string{
		"gzip",
		"gzip;q=0",
		"br, gzip;q=0.5",
		"*;q=0",
		"GZIP;Q=1",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(_ *testing.T, accept string) {
		_ = acceptsGzip(accept)
	})
}

func FuzzForwardedEntry(f *testing.F) {
	for _, seed := range []string{
		`for=192.0.2.1;proto=https`,
		`for="[2001:db8::1]:443";proto=HTTP`,
		`for=unknown;proto=file`,
		``,
		`for="::ffff:1.2.3.4"`,
		`for="[fe80::1%en0]:80"`,
		`for=1.2.3.4:0`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, entry string) {
		h := parseEntry(entry, true)
		if h.proto != "" && h.proto != "http" && h.proto != "https" {
			t.Fatalf("scheme = %q, want a normalized HTTP scheme or none", h.proto)
		}
		_, _, parsed := parseHop(h.addr)
		ap, _, split := splitHop(h.addr)
		if parsed != split {
			t.Fatalf("parseHop(%q) ok = %t, splitHop ok = %t", h.addr, parsed, split)
		}
		if split {
			a := ap.Addr().WithZone("").Unmap()
			if !a.IsValid() || a.Is4In6() || a.Zone() != "" {
				t.Fatalf("normalized %q = %v, want a valid address with no mapping and no zone", h.addr, a)
			}
		}
		parseEntry(entry, false)
	})
}

func FuzzForwardedProto(f *testing.F) {
	for _, seed := range []struct {
		line1, line2 string
		leftmost     bool
	}{
		{"https", "", false},
		{"HTTPS, http", "", false},
		{"https", "http", true},
		{"gopher", "", false},
		{"", "", false},
		{" , https", "http ,", true},
	} {
		f.Add(seed.line1, seed.line2, seed.leftmost)
	}
	f.Fuzz(func(t *testing.T, line1, line2 string, leftmost bool) {
		var values []string
		for _, v := range []string{line1, line2} {
			if v != "" {
				values = append(values, v)
			}
		}
		proto, fix := keptProto(leftmost, "", values)
		if proto != "" && proto != "http" && proto != "https" {
			t.Fatalf("scheme = %q, want a normalized HTTP scheme or none", proto)
		}
		unchanged := (len(values) == 0 && proto == "") || (len(values) == 1 && values[0] == proto)
		if fix == unchanged {
			t.Fatalf("keptProto(%t, %q) fix = %t for scheme %q", leftmost, values, fix, proto)
		}
	})
}
