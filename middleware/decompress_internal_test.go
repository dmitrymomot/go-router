package middleware

import (
	"bytes"
	"compress/zlib"
	"testing"
	"weak"
)

func TestContentEncoding(t *testing.T) {
	tests := []struct {
		values []string
		want   encoding
	}{
		{nil, encodingNone},
		{[]string{""}, encodingNone},
		{[]string{"identity"}, encodingNone},
		{[]string{"gzip"}, encodingGzip},
		{[]string{" X-Gzip "}, encodingGzip},
		{[]string{"identity, gzip"}, encodingGzip},
		{[]string{"DEFLATE"}, encodingDeflate},
		{[]string{"br"}, encodingUnknown},
		{[]string{"gzip, gzip"}, encodingUnknown},
		{[]string{"gzip", "deflate"}, encodingUnknown},
		{[]string{"gzip, br"}, encodingUnknown},
	}
	for _, tt := range tests {
		if got := contentEncoding(tt.values); got != tt.want {
			t.Errorf("contentEncoding(%q) = %d, want %d", tt.values, got, tt.want)
		}
	}
}

func TestZlibReaderPoolDetachesTheRequestBody(t *testing.T) {
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write([]byte("expand me")); err != nil {
		t.Fatalf("write zlib payload: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zlib payload: %v", err)
	}

	token := &retentionToken{}
	pointer := weak.Make(token)
	source := &retainingReader{Reader: bytes.NewReader(compressed.Bytes()), token: token}
	zr, err := zlib.NewReader(source)
	if err != nil {
		t.Fatalf("open zlib reader: %v", err)
	}
	detachZlibReader(zr)

	requireCollected(t, pointer, zr)
}
