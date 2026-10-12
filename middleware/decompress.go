package middleware

import (
	"bufio"
	"cmp"
	"compress/gzip"
	"compress/zlib"
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

// zlibReaders holds the readers of deflate bodies. It has no New, since
// zlib.NewReader needs a stream to read the header of.
var zlibReaders sync.Pool

const (
	emptyGzipStream = "\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\x03\x03\x00\x00\x00\x00\x00\x00\x00\x00\x00"
	emptyZlibStream = "\x78\x9c\x03\x00\x00\x00\x00\x01"
)

// acceptedEncodings is the Accept-Encoding of a 415 that [Decompress] answers.
const acceptedEncodings = "gzip, deflate"

// Decompress expands a request body that Content-Encoding marks as gzip or
// deflate, so the handler and the binders read plain bytes. A body in no
// encoding, or in identity, passes through.
//
// Any other encoding, and more than one stacked, as in "gzip, gzip", is
// refused with [router.ErrUnsupportedMediaType] and an Accept-Encoding of
// "gzip, deflate", since the handler could not read the body anyway.
//
// The expansion stops at [router.DefaultMaxBodyBytes], which is what keeps a
// small body that expands to a huge one from filling the memory. A body that
// does not decode reports [router.ErrBadRequest], and one over the limit
// reports [router.ErrPayloadTooLarge] and closes the connection after the
// answer.
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
			if skipped(cfg.Skip, c) || req.Body == nil || req.Body == http.NoBody {
				return next(c)
			}
			enc := contentEncoding(req.Header.Values(router.HeaderContentEncoding))
			switch enc {
			case encodingNone:
				return next(c)
			case encodingUnknown:
				c.Response().Header().Set(router.HeaderAcceptEncoding, acceptedEncodings)
				return router.ErrUnsupportedMediaType.WithMessage("the Content-Encoding %q is not supported",
					strings.Join(req.Header.Values(router.HeaderContentEncoding), ", "))
			}

			// Both readers wrap a source without ReadByte in a fresh 4 KiB
			// bufio.Reader, and *http.body has none.
			src := byteReaders.Get().(*bufio.Reader)
			src.Reset(req.Body)
			if _, err := src.Peek(1); errors.Is(err, io.EOF) {
				putByteReader(src)
				return next(c)
			}

			zr, err := openDecoder(enc, src)
			if err != nil {
				putByteReader(src)
				return router.ErrBadRequest.WithMessage("malformed %s body", enc).WithError(err)
			}

			done := false
			body := &decompressedBody{zr: zr, src: req.Body}
			defer func() {
				body.zr = nil
				if done {
					putDecoder(zr)
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

			err = next(c)
			done = true
			return tooLarge(err, "the expanded request body is limited to %d bytes", limit)
		}
	}
}

type bodyCoding uint8

const (
	encodingNone bodyCoding = iota
	encodingGzip
	encodingDeflate
	encodingUnknown
)

func (e bodyCoding) String() string {
	if e == encodingDeflate {
		return "deflate"
	}
	return "gzip"
}

// contentEncoding reads the Content-Encoding of a request. identity counts as
// no encoding, and a second coding makes the whole unknown.
func contentEncoding(values []string) bodyCoding {
	enc := encodingNone
	for _, v := range values {
		for e := range strings.SplitSeq(v, ",") {
			var found bodyCoding
			switch e = strings.TrimSpace(e); {
			case e == "" || strings.EqualFold(e, "identity"):
				continue
			case strings.EqualFold(e, "gzip") || strings.EqualFold(e, "x-gzip"):
				found = encodingGzip
			case strings.EqualFold(e, "deflate"):
				found = encodingDeflate
			default:
				return encodingUnknown
			}
			if enc != encodingNone {
				return encodingUnknown
			}
			enc = found
		}
	}
	return enc
}

// openDecoder takes a pooled reader of enc and points it at src. A reader
// that fails is back in its pool when it returns.
func openDecoder(enc bodyCoding, src *bufio.Reader) (io.Reader, error) {
	if enc == encodingGzip {
		zr := gzipReaders.Get().(*gzip.Reader)
		if err := zr.Reset(src); err != nil {
			putDecoder(zr)
			return nil, err
		}
		return zr, nil
	}
	if zr, ok := zlibReaders.Get().(io.Reader); ok {
		if err := zr.(zlib.Resetter).Reset(src, nil); err != nil {
			putDecoder(zr)
			return nil, err
		}
		return zr, nil
	}
	return zlib.NewReader(src)
}

// putDecoder detaches zr from its source, so the pool does not keep the body
// alive, and puts it back.
func putDecoder(zr io.Reader) {
	if gz, ok := zr.(*gzip.Reader); ok {
		detachGzipReader(gz)
		gzipReaders.Put(gz)
		return
	}
	detachZlibReader(zr)
	zlibReaders.Put(zr)
}

var byteReaders = sync.Pool{New: func() any { return bufio.NewReader(nil) }}

func putByteReader(r *bufio.Reader) {
	r.Reset(nil)
	byteReaders.Put(r)
}

func detachGzipReader(zr *gzip.Reader) {
	_ = zr.Reset(strings.NewReader(emptyGzipStream))
}

func detachZlibReader(zr io.Reader) {
	_ = zr.(zlib.Resetter).Reset(strings.NewReader(emptyZlibStream), nil)
}

type decompressedBody struct {
	zr  io.Reader
	src io.ReadCloser
}

func (b *decompressedBody) Read(p []byte) (int, error) {
	if b.zr == nil {
		return 0, http.ErrBodyReadAfterClose
	}
	return b.zr.Read(p)
}

func (b *decompressedBody) Close() error { return b.src.Close() }
