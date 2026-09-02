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
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, entry string) {
		h := parseEntry(entry, true)
		if h.proto != "" && h.proto != "http" && h.proto != "https" {
			t.Fatalf("scheme = %q, want a normalized HTTP scheme or none", h.proto)
		}
		parseHop(h.addr)
		parseEntry(entry, false)
	})
}
