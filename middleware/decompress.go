package middleware

import (
	"bufio"
	"cmp"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/internal/routerhook"
)

// DecompressConfig configures [DecompressWithConfig]. MaxDecompressedSize is
// how many bytes a body expands to before the expansion stops, and zero takes
// [router.DefaultMaxBodyBytes].
type DecompressConfig[C router.Context] struct {
	Skip                func(c C) bool
	MaxDecompressedSize int64
}

var gzipReaders = sync.Pool{New: func() any { return new(gzip.Reader) }}

const emptyGzipStream = "\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\x03\x03\x00\x00\x00\x00\x00\x00\x00\x00\x00"

// Decompress expands a request body that Content-Encoding marks as gzip, so
// the handler and the binders read plain bytes. A body in any other encoding,
// and one in none, passes through.
//
// The expansion stops at [router.DefaultMaxBodyBytes], which is what keeps a
// small body that expands to a huge one from filling the memory. A body that
// is not gzip reports [router.ErrBadRequest], and one over the limit reports
// [router.ErrPayloadTooLarge] and closes the connection after the answer.
//
// See Order in the package doc for where it goes.
func Decompress[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return DecompressWithConfig(DecompressConfig[C]{})(next)
}

// DecompressWithConfig is [Decompress] with a configuration.
//
// DecompressWithConfig panics on a negative MaxDecompressedSize.
func DecompressWithConfig[C router.Context](cfg DecompressConfig[C]) router.Middleware[C] {
	if cfg.MaxDecompressedSize < 0 {
		panic("middleware: DecompressWithConfig needs a MaxDecompressedSize of zero or more")
	}
	limit := cmp.Or(cfg.MaxDecompressedSize, router.DefaultMaxBodyBytes)

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			req := c.Request()
			if skipped(cfg.Skip, c) || req.Body == nil || req.Body == http.NoBody ||
				!isGzipEncoding(req.Header.Get(router.HeaderContentEncoding)) {
				return next(c)
			}

			// gzip.Reader.Reset wraps a source without ReadByte in a fresh 4 KiB
			// bufio.Reader, and *http.body has none.
			src := byteReaders.Get().(*bufio.Reader)
			src.Reset(req.Body)

			zr := gzipReaders.Get().(*gzip.Reader)
			if err := zr.Reset(src); err != nil {
				putByteReader(src)
				putGzipReader(zr)
				if errors.Is(err, io.EOF) {
					return next(c)
				}
				return router.ErrBadRequest.WithMessage("malformed gzip body").WithError(err)
			}

			done := false
			body := &decompressedBody{zr: zr, src: req.Body}
			defer func() {
				body.zr = nil
				if done {
					putGzipReader(zr)
					putByteReader(src)
				}
			}()

			expanded := *req
			expanded.Body = http.MaxBytesReader(routerhook.InnermostWriter(c.Response()), body, limit)
			expanded.ContentLength = -1
			expanded.Header = req.Header.Clone()
			expanded.Header.Del(router.HeaderContentEncoding)
			expanded.Header.Del(router.HeaderContentLength)
			c.SetRequest(&expanded)

			err := next(c)
			done = true
			return tooLarge(err, "the expanded request body is limited to %d bytes", limit)
		}
	}
}

var byteReaders = sync.Pool{New: func() any { return bufio.NewReader(nil) }}

func putByteReader(r *bufio.Reader) {
	r.Reset(nil)
	byteReaders.Put(r)
}

func putGzipReader(zr *gzip.Reader) {
	detachGzipReader(zr)
	gzipReaders.Put(zr)
}

func detachGzipReader(zr *gzip.Reader) {
	_ = zr.Reset(strings.NewReader(emptyGzipStream))
}

type decompressedBody struct {
	zr  *gzip.Reader
	src io.ReadCloser
}

func (b *decompressedBody) Read(p []byte) (int, error) {
	if b.zr == nil {
		return 0, http.ErrBodyReadAfterClose
	}
	return b.zr.Read(p)
}

func (b *decompressedBody) Close() error { return b.src.Close() }

func isGzipEncoding(encoding string) bool {
	e := strings.TrimSpace(encoding)
	return strings.EqualFold(e, "gzip") || strings.EqualFold(e, "x-gzip")
}
