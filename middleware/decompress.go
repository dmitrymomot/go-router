package middleware

import (
	"bufio"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/dmitrymomot/go-router"
)

const DefaultMaxDecompressedSize int64 = 100 << 20

type DecompressConfig struct {
	Skip                func(c router.Context) bool
	MaxDecompressedSize int64
}

var gzipReaders = sync.Pool{New: func() any { return new(gzip.Reader) }}

const emptyGzipStream = "\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\x03\x03\x00\x00\x00\x00\x00\x00\x00\x00\x00"

func Decompress[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return DecompressWithConfig[C](DecompressConfig{})(next)
}

func DecompressWithConfig[C router.Context](cfg DecompressConfig) router.Middleware[C] {
	limit := cfg.MaxDecompressedSize
	if limit == 0 {
		limit = DefaultMaxDecompressedSize
	}

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
			var rc io.ReadCloser = body
			if limit > 0 {
				rc = http.MaxBytesReader(c.Response(), rc, limit)
			}
			expanded.Body = rc
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
