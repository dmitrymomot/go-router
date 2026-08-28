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

// DefaultMaxDecompressedSize is the expanded body that [Decompress] allows.
// Ten kilobytes of zeros compress to a few dozen bytes, so a cap on the body
// that arrives is no cap at all on the body that a handler reads.
const DefaultMaxDecompressedSize int64 = 100 << 20 // 100 MiB

// DecompressConfig configures [DecompressWithConfig].
type DecompressConfig struct {
	// Skip passes a request straight to the next handler when it returns true.
	Skip func(c router.Context) bool

	// MaxDecompressedSize is the largest expanded body in bytes. Zero uses
	// [DefaultMaxDecompressedSize], and a negative value lifts the cap.
	MaxDecompressedSize int64
}

// gzipReaders holds the readers that [DecompressWithConfig] expands with. A
// [gzip.Reader] carries a 32 KiB window, which is worth keeping across
// requests.
var gzipReaders = sync.Pool{New: func() any { return new(gzip.Reader) }}

// Decompress is [DecompressWithConfig] with its default config, which caps the
// expanded body at [DefaultMaxDecompressedSize]. It is a middleware itself, so
// it goes into Use without a call:
//
//	r.Use(middleware.Decompress[Ctx])
func Decompress[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return DecompressWithConfig[C](DecompressConfig{})(next)
}

// DecompressWithConfig expands a request body that carries
// Content-Encoding: gzip.
//
// It replaces [http.Request.Body] with the expanded stream and sets
// ContentLength to -1, so every reader below it sees a plain body: Bind, a
// handler that reads the body itself, and a standard handler alike. It also
// drops the Content-Encoding and Content-Length headers, because neither
// describes the body any more.
//
// The expanded body stops at MaxDecompressedSize with
// [router.ErrPayloadTooLarge]. That cap is the one that counts once a body
// expands: [BodyLimit] and [router.Router.MaxBodyBytes] measure the bytes that
// arrive, and a few hundred of those become megabytes. Put [BodyLimit] above
// this middleware to cap both halves.
//
// A body that is empty, that names another encoding, or that names several,
// passes through untouched. A body that names gzip and is not gzip produces a
// 400.
//
// The expanded body lasts as long as the chain. The reader that expands it
// goes back to a pool when this middleware returns, so a read from the error
// handler, from an observer or from a middleware above this one reports
// [http.ErrBodyReadAfterClose]. Read the body while the chain still runs and
// keep what you need of it.
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
					// The body is empty, so there is nothing to expand and
					// nothing to report.
					return next(c)
				}
				return router.ErrBadRequest.WithMessage("malformed gzip body").WithError(err)
			}

			// A panic leaves the reader in the middle of a stream, and a
			// poisoned reader in the pool would follow the next request around.
			done := false
			body := &decompressedBody{zr: zr, src: req.Body}
			defer func() {
				// The request that this middleware installs outlives the chain:
				// the error handler, an observer and a middleware above this one
				// all run after it returns, and any of them may read the body.
				// Cut the link first, so a read after the reader went back to
				// the pool reports a closed body instead of the stream of
				// whichever request holds the reader by then.
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
			// The shallow copy shares the header map with the request that came
			// in, which belongs to the server, so the two headers that no longer
			// describe this body come off a copy of it.
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

// decompressedBody is the expanded request body. Close closes the compressed
// body underneath, which is the one that holds the connection; the reader
// itself goes back to the pool when the chain returns, and zr is nil from then
// on. The server closes the body it made rather than this one, so Close cannot
// carry either of those.
type decompressedBody struct {
	zr  *gzip.Reader
	src io.ReadCloser
}

// Read implements [io.Reader]. It reports a closed body once the chain that
// expanded it has returned, because the reader belongs to another request by
// then.
func (b *decompressedBody) Read(p []byte) (int, error) {
	if b.zr == nil {
		return 0, http.ErrBodyReadAfterClose
	}
	return b.zr.Read(p)
}

// Close implements [io.Closer].
func (b *decompressedBody) Close() error { return b.src.Close() }

// isGzipEncoding reports whether the Content-Encoding header names gzip and
// nothing else. A header that stacks two encodings names a body that one pass
// does not expand, so it passes through instead.
func isGzipEncoding(encoding string) bool {
	e := strings.TrimSpace(encoding)
	return strings.EqualFold(e, "gzip") || strings.EqualFold(e, "x-gzip")
}
