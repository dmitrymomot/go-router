package static_test

import (
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/dmitrymomot/go-router/static"
)

func TestFSSetRefusesDotfiles(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":      {Data: []byte(rootIndex)},
		".env":            {Data: []byte("secret")},
		".git/config":     {Data: []byte("secret")},
		"app/.hidden/key": {Data: []byte("secret")},
		"app.js":          {Data: []byte(appJS)},
	}
	a := newAssets(t, static.Config{FS: fsys})
	for _, target := range []string{"/.env", "/.git/config", "/app/.hidden/key"} {
		if rec := get(a, target); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
	}
	if a.Has(".env") {
		t.Error("Has(.env) = true, want false")
	}
	if rec := get(a, "/app.js"); rec.Code != http.StatusOK {
		t.Errorf("GET /app.js = %d, want 200", rec.Code)
	}

	opened := newAssets(t, static.Config{FS: fsys, Dotfiles: true})
	if rec := get(opened, "/.env"); rec.Code != http.StatusOK {
		t.Errorf("GET /.env with Dotfiles = %d, want 200", rec.Code)
	}
	if a.Build() == opened.Build() {
		t.Error("the dotfiles did not change the build tag of the set that holds them")
	}
}

func precompressedFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":   {Data: []byte(rootIndex)},
		"app.js":       {Data: []byte(appJS)},
		"app.js.br":    {Data: []byte("BROTLI-BYTES")},
		"app.js.gz":    {Data: []byte("GZIP-BYTES")},
		"plain.css":    {Data: []byte(appCSS)},
		"only.txt.gz":  {Data: []byte("kept as a file")},
		"data.unknown": {Data: []byte("raw")},
		"data.unknown.gz": {
			Data: []byte("zipped"),
		},
	}
}

func TestPrecompressedSiblings(t *testing.T) {
	a := newAssets(t, static.Config{FS: precompressedFS()})

	tests := []struct {
		name         string
		target       string
		acceptEnc    string
		wantBody     string
		wantEncoding string
		wantVary     bool
	}{
		{"br wins over gzip", "/app.js", "gzip, br", "BROTLI-BYTES", "br", true},
		{"gzip alone", "/app.js", "gzip", "GZIP-BYTES", "gzip", true},
		{"q=0 refuses br", "/app.js", "br;q=0, gzip", "GZIP-BYTES", "gzip", true},
		{"q=0 refuses both", "/app.js", "br;q=0, gzip;q=0", appJS, "", true},
		{"star admits", "/app.js", "*", "BROTLI-BYTES", "br", true},
		{"no header", "/app.js", "", appJS, "", true},
		{"deflate only", "/app.js", "deflate", appJS, "", true},
		{"no sibling no vary", "/plain.css", "br, gzip", appCSS, "", false},
		{"unknown type is not sniffed", "/data.unknown", "gzip", "zipped", "gzip", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []reqOption
			if tt.acceptEnc != "" {
				opts = append(opts, header("Accept-Encoding", tt.acceptEnc))
			}
			rec := get(a, tt.target, opts...)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
			if got := rec.Header().Get("Content-Encoding"); got != tt.wantEncoding {
				t.Errorf("Content-Encoding = %q, want %q", got, tt.wantEncoding)
			}
			if got := strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding"); got != tt.wantVary {
				t.Errorf("Vary = %q, want Accept-Encoding: %v", rec.Header().Get("Vary"), tt.wantVary)
			}
			if tt.wantEncoding != "" {
				if tag := rec.Header().Get("Etag"); !strings.HasSuffix(tag, "-"+map[string]string{"br": "br", "gzip": "gz"}[tt.wantEncoding]+`"`) {
					t.Errorf("Etag = %q, want the encoding as a suffix", tag)
				}
			}
		})
	}

	br := get(a, "/app.js", header("Accept-Encoding", "br"))
	if got := br.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Errorf("Content-Type = %q, want the one of app.js", got)
	}
	if got := br.Header().Get("Etag"); !strings.HasSuffix(got, `-br"`) {
		t.Errorf("Etag = %q, want the -br suffix", got)
	}
	if got := get(a, "/data.unknown", header("Accept-Encoding", "gzip")).Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want octet-stream rather than a sniffed gzip", got)
	}
}

func TestPrecompressedConditionalAndRange(t *testing.T) {
	a := newAssets(t, static.Config{FS: precompressedFS()})
	enc := header("Accept-Encoding", "br")

	tag := get(a, "/app.js", enc).Header().Get("Etag")
	if rec := get(a, "/app.js", enc, header("If-None-Match", tag)); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match = %d, want 304", rec.Code)
	}
	plainTag := get(a, "/app.js").Header().Get("Etag")
	if rec := get(a, "/app.js", enc, header("If-None-Match", plainTag)); rec.Code != http.StatusOK {
		t.Errorf("the identity ETag matched the br file: %d", rec.Code)
	}

	rec := get(a, "/app.js", enc, header("Range", "bytes=0-4"))
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "BROTL" {
		t.Errorf("Range = %d %q, want 206 BROTL", rec.Code, rec.Body.String())
	}
}

func TestPrecompressedSiblingsAreNotAssets(t *testing.T) {
	a := newAssets(t, static.Config{FS: precompressedFS()})

	for _, name := range []string{"app.js.br", "app.js.gz"} {
		if a.Has(name) {
			t.Errorf("Has(%s) = true, want false", name)
		}
		if rec := get(a, "/"+name); rec.Code != http.StatusNotFound {
			t.Errorf("GET /%s = %d, want 404", name, rec.Code)
		}
	}
	// A .gz with no file beside it is an ordinary asset.
	if !a.Has("only.txt.gz") {
		t.Error("Has(only.txt.gz) = false, want true")
	}
	if rec := get(a, "/only.txt.gz"); rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("GET /only.txt.gz = %d with encoding %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
}
