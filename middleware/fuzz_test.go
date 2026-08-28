package middleware

import (
	"net/url"
	"strings"
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

func FuzzRewriteEscapedCapture(f *testing.F) {
	for _, seed := range []string{
		"a/b",
		`a\b`,
		"a%252Fb",
		"résumé",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, capture string) {
		escaped := url.PathEscape(capture)
		rule := rewriteRule{parts: []string{"/old/", ""}, to: "/new/$1"}
		got, ok := rule.apply("/old/" + escaped)
		if !ok {
			t.Fatal("the trailing capture did not match")
		}
		decoded, err := url.PathUnescape(got)
		if err != nil {
			t.Fatalf("rewritten path is not a valid escape: %v", err)
		}
		if want := "/new/" + capture; decoded != want {
			t.Fatalf("decoded rewrite = %q, want %q", decoded, want)
		}
		if strings.Contains(capture, "/") && !strings.Contains(got, "%2F") {
			t.Fatalf("escaped slash was lost: %q", got)
		}
	})
}
