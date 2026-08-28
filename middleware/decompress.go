package middleware

import (
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
	Skip func(c router.Context) bool

	MaxDecompressedSize int64
}

var gzipReaders = sync.Pool{New: func() any { return new(gzip.Reader) }}

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

			zr := gzipReaders.Get().(*gzip.Reader)
			if err := zr.Reset(req.Body); err != nil {
				gzipReaders.Put(zr)
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
					gzipReaders.Put(zr)
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
