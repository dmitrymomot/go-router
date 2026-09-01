package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"runtime"
	"sync"
	"testing"
	"weak"
)

type retentionToken [64]byte

type retainingResponseWriter struct {
	header http.Header
	token  *retentionToken
}

func (w *retainingResponseWriter) Header() http.Header         { return w.header }
func (w *retainingResponseWriter) WriteHeader(int)             {}
func (w *retainingResponseWriter) Write(p []byte) (int, error) { return len(p), nil }

type retainingReader struct {
	*bytes.Reader
	token *retentionToken
}

func requireCollected(t *testing.T, pointer weak.Pointer[retentionToken], keepAlive any) {
	t.Helper()
	for range 20 {
		runtime.GC()
		if pointer.Value() == nil {
			runtime.KeepAlive(keepAlive)
			return
		}
		runtime.Gosched()
	}
	runtime.KeepAlive(keepAlive)
	t.Fatal("a pooled compressor still retains the completed request graph")
}

func TestGzipWriterPoolDetachesTheResponseWriter(t *testing.T) {
	pool := &sync.Pool{New: func() any {
		writer, err := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
		if err != nil {
			t.Fatalf("create gzip writer: %v", err)
		}
		return writer
	}}
	token := &retentionToken{}
	pointer := weak.Make(token)
	underlying := &retainingResponseWriter{header: make(http.Header), token: token}
	w := &gzipWriter{ResponseWriter: underlying, pool: pool, min: 1}
	if _, err := w.Write([]byte("compress me")); err != nil {
		t.Fatalf("write compressed response: %v", err)
	}
	w.finish(true)
	pooled := pool.Get().(*gzip.Writer)

	requireCollected(t, pointer, pooled)
}

func TestGzipReaderPoolDetachesTheRequestBody(t *testing.T) {
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write([]byte("expand me")); err != nil {
		t.Fatalf("write gzip payload: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip payload: %v", err)
	}

	token := &retentionToken{}
	pointer := weak.Make(token)
	source := &retainingReader{Reader: bytes.NewReader(compressed.Bytes()), token: token}
	zr := new(gzip.Reader)
	if err := zr.Reset(source); err != nil {
		t.Fatalf("reset gzip reader: %v", err)
	}
	detachGzipReader(zr)

	requireCollected(t, pointer, zr)
}
