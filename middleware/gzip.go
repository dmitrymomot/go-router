package middleware

import (
	"cmp"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/dmitrymomot/go-router"
)

// DefaultGzipMinLength is the body below which [Gzip] compresses nothing,
// because the header of the format costs more than the saving.
const DefaultGzipMinLength = 1024

// GzipConfig configures [GzipWithConfig]. Level is a level of [compress/gzip]
// from [gzip.HuffmanOnly] to [gzip.BestCompression], and zero takes
// [gzip.DefaultCompression]; [gzip.NoCompression] cannot be asked for, since
// Skip does that better. MinLength is the shortest body worth compressing, and
// zero takes [DefaultGzipMinLength].
type GzipConfig[C router.Context] struct {
	Skip      func(c C) bool
	Level     int
	MinLength int
}

// Gzip compresses the response for a client that says it takes gzip, and adds
// Accept-Encoding to Vary. A body under [DefaultGzipMinLength], a status with
// no body, a body that is already encoded, and an answer whose Cache-Control
// says no-transform all go out untouched.
//
// A compressed answer drops Accept-Ranges, since a range of the compressed
// bytes is not a range of the resource. Its ETag gets the suffix "-gzip" inside
// the quotes, so "abc" becomes "abc-gzip" and W/"abc" becomes W/"abc-gzip":
// the compressed bytes are a representation of their own, with a tag of their
// own. For a client that takes gzip, Gzip drops the suffix from the tags of
// If-None-Match and If-Match before the handler reads them, so the handler
// compares its own tags. A 304 carries the tag the client holds: the suffixed
// one when If-None-Match named it, or, with no If-None-Match, whenever
// Cache-Control allows a transform.
//
// A stream passes through: an event stream is never compressed, and a handler
// that flushes keeps its data moving to the client.
//
// A HEAD gets the headers its GET would carry and no body.
//
// Put it outside [Idempotency], so a replay is compressed for the client that
// asks again; see Order in the package doc.
func Gzip[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return GzipWithConfig(GzipConfig[C]{})(next)
}

// GzipWithConfig is [Gzip] with a configuration.
//
// GzipWithConfig panics on a Level out of range and on a negative MinLength.
func GzipWithConfig[C router.Context](cfg GzipConfig[C]) router.Middleware[C] {
	level := cfg.Level
	switch {
	case level == 0:
		level = gzip.DefaultCompression
	case level < gzip.HuffmanOnly || level > gzip.BestCompression:
		panic(fmt.Sprintf("middleware: GzipWithConfig got the Level %d; "+
			"take one from %d to %d, or zero for the default", level, gzip.HuffmanOnly, gzip.BestCompression))
	}
	if cfg.MinLength < 0 {
		panic("middleware: GzipWithConfig needs a MinLength of zero or more")
	}
	minLength := cmp.Or(cfg.MinLength, DefaultGzipMinLength)
	pool := &sync.Pool{New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, level)
		return w
	}}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			res := c.Response()
			router.AddVary(res.Header(), router.HeaderAcceptEncoding)

			req := c.Request()
			if !acceptsGzip(req.Header.Get(router.HeaderAcceptEncoding)) {
				return next(c)
			}

			w := &gzipWriter{
				ResponseWriter: res.ResponseWriter,
				res:            res,
				pool:           pool,
				min:            minLength,
				head:           req.Method == http.MethodHead,
			}
			w.revalidating, w.unsuffixed = unsuffixGzipETags(req.Header, router.HeaderIfNoneMatch)
			unsuffixGzipETags(req.Header, headerIfMatch)
			before := res.Size
			res.ResponseWriter = w

			ok := false
			defer func() {
				w.finish(ok)
				res.ResponseWriter = w.ResponseWriter
				res.Size = before + w.written
			}()

			err := next(c)
			ok = true
			return err
		}
	}
}

const (
	gzipUndecided uint8 = iota
	gzipPlain
	gzipOn
)

type gzipWriter struct {
	http.ResponseWriter
	res     *router.Response
	pool    *sync.Pool
	gz      *gzip.Writer
	buf     []byte
	written int64
	min     int
	code    int
	state   uint8
	head    bool

	// revalidating says that the request sent If-None-Match, and unsuffixed
	// holds its tags that carried the suffix of a compressed answer.
	revalidating bool
	unsuffixed   []string
}

func (w *gzipWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *gzipWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.code != 0 {
		return
	}
	w.code = code

	h := w.Header()
	if code == http.StatusNotModified {
		w.tagNotModified(h)
	}
	if !compressibleStatus(code) ||
		h.Get(router.HeaderContentEncoding) != "" ||
		isEventStream(h.Get(router.HeaderContentType)) ||
		noTransform(h.Values(router.HeaderCacheControl)) {
		//nolint:errcheck // Nothing writes a status line and reads an error.
		w.commit(false)
		return
	}
	if n, err := strconv.ParseInt(h.Get(router.HeaderContentLength), 10, 64); err == nil {
		if n >= int64(w.min) && h.Get(router.HeaderContentType) == "" {
			// Committing now would set Content-Encoding with nothing buffered
			// to sniff, and net/http stops sniffing once it is set. Wait for
			// the first Write.
			return
		}
		//nolint:errcheck // Same as above: the body follows, and reports it.
		w.commit(n >= int64(w.min))
	}
}

func (w *gzipWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.WriteHeader(http.StatusOK)
	}
	switch w.state {
	case gzipOn:
		if w.head {
			return len(p), nil
		}
		return w.gz.Write(p)
	case gzipPlain:
		if w.head {
			return len(p), nil
		}
		n, err := w.ResponseWriter.Write(p)
		w.written += int64(n)
		return n, err
	}

	if w.buf == nil {
		w.buf = make([]byte, 0, max(w.min, len(p)))
	}
	w.buf = append(w.buf, p...)
	if len(w.buf) < w.min {
		return len(p), nil
	}
	return len(p), w.commit(true)
}

func (w *gzipWriter) Flush() {
	if w.code == 0 {
		if w.res.Status == 0 {
			w.res.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(w.res.Status)
		}
	}
	switch w.state {
	case gzipUndecided:
		//nolint:errcheck // Flush reports nothing, as [http.Flusher] does.
		w.commit(true)
	case gzipOn:
		if w.gz != nil {
			//nolint:errcheck // Same as above.
			w.gz.Flush()
		}
	}
	//nolint:errcheck // A writer that cannot flush says so and changes nothing.
	http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *gzipWriter) commit(compress bool) error {
	code := w.code
	if code == 0 {
		code = http.StatusOK
	}
	w.code = code

	if !compress {
		w.state = gzipPlain
		w.ResponseWriter.WriteHeader(code)
		if w.head {
			w.buf = w.buf[:0]
			return nil
		}
		if len(w.buf) == 0 {
			return nil
		}
		n, err := w.ResponseWriter.Write(w.buf)
		w.written += int64(n)
		w.buf = w.buf[:0]
		return err
	}

	h := w.Header()
	if h.Get(router.HeaderContentType) == "" && len(w.buf) > 0 {
		h.Set(router.HeaderContentType, http.DetectContentType(w.buf))
	}
	h.Del(router.HeaderContentLength)
	h.Del(headerAcceptRanges)
	if etag := h.Get(router.HeaderETag); etag != "" {
		h.Set(router.HeaderETag, suffixGzipETag(etag))
	}
	h.Set(router.HeaderContentEncoding, "gzip")

	w.state = gzipOn
	if w.head {
		w.ResponseWriter.WriteHeader(code)
		w.buf = w.buf[:0]
		return nil
	}
	w.gz = w.pool.Get().(*gzip.Writer)
	w.gz.Reset(gzipSink{w})
	w.ResponseWriter.WriteHeader(code)
	if len(w.buf) == 0 {
		return nil
	}
	_, err := w.gz.Write(w.buf)
	w.buf = w.buf[:0]
	return err
}

func (w *gzipWriter) finish(ok bool) {
	switch w.state {
	case gzipUndecided:
		if w.code == 0 && len(w.buf) == 0 {
			return
		}
		//nolint:errcheck // The response ends here; nothing reads the error.
		w.commit(false)
	case gzipOn:
		if !ok || w.head {
			return
		}
		//nolint:errcheck // The response is committed; nothing reads the error.
		w.gz.Close()
		w.gz.Reset(io.Discard)
		w.pool.Put(w.gz)
		w.gz = nil
	}
}

// tagNotModified gives a 304 the ETag of the answer the client holds; see
// [Gzip].
func (w *gzipWriter) tagNotModified(h http.Header) {
	etag := h.Get(router.HeaderETag)
	if etag == "" || h.Get(router.HeaderContentEncoding) != "" {
		return
	}
	suffix := !noTransform(h.Values(router.HeaderCacheControl))
	if w.revalidating {
		suffix = slices.Contains(w.unsuffixed, strings.TrimPrefix(etag, "W/"))
	}
	if suffix {
		h.Set(router.HeaderETag, suffixGzipETag(etag))
	}
}

type gzipSink struct{ w *gzipWriter }

func (s gzipSink) Write(p []byte) (int, error) {
	n, err := s.w.ResponseWriter.Write(p)
	s.w.written += int64(n)
	return n, err
}

const (
	headerAcceptRanges = "Accept-Ranges"
	headerIfMatch      = "If-Match"
)

// gzipETagSuffix marks the ETag of a compressed answer.
const gzipETagSuffix = "-gzip"

// suffixGzipETag puts [gzipETagSuffix] inside the quotes of an entity tag. A
// value that is not a quoted tag stays as it is.
func suffixGzipETag(etag string) string {
	tag := strings.TrimPrefix(etag, "W/")
	if len(tag) < 2 || tag[0] != '"' || tag[len(tag)-1] != '"' {
		return etag
	}
	return etag[:len(etag)-1] + gzipETagSuffix + `"`
}

// unsuffixGzipETags drops [gzipETagSuffix] from each entity tag of the header
// field name. It reports whether the field is present, and the opaque tags,
// without W/, that lost the suffix. A field it cannot parse stays as it is.
func unsuffixGzipETags(h http.Header, name string) (present bool, unsuffixed []string) {
	values := h.Values(name)
	if len(values) == 0 {
		return false, nil
	}
	var tags []string
	for _, v := range values {
		for v = trimETagSeparators(v); v != ""; v = trimETagSeparators(v) {
			if v[0] == '*' {
				tags = append(tags, "*")
				v = v[1:]
				continue
			}
			weak := strings.HasPrefix(v, "W/")
			opaque := strings.TrimPrefix(v, "W/")
			end := strings.IndexByte(opaque[min(1, len(opaque)):], '"') + 1
			if len(opaque) == 0 || opaque[0] != '"' || end == 0 {
				return true, nil
			}
			v = opaque[end+1:]
			opaque = opaque[:end+1]
			if plain, ok := strings.CutSuffix(opaque, gzipETagSuffix+`"`); ok {
				opaque = plain + `"`
				unsuffixed = append(unsuffixed, opaque)
			}
			if weak {
				opaque = "W/" + opaque
			}
			tags = append(tags, opaque)
		}
	}
	if len(unsuffixed) > 0 {
		h.Set(name, strings.Join(tags, ", "))
	}
	return true, unsuffixed
}

func trimETagSeparators(s string) string {
	return strings.TrimLeft(s, " \t,")
}

// noTransform reports whether a Cache-Control header forbids a proxy, and so
// this middleware, to change the encoding of the body.
func noTransform(cacheControl []string) bool {
	for _, v := range cacheControl {
		for d := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(d), "no-transform") {
				return true
			}
		}
	}
	return false
}

func compressibleStatus(code int) bool {
	switch code {
	case http.StatusSwitchingProtocols,
		http.StatusNoContent, http.StatusNotModified, http.StatusPartialContent:
		return false
	default:
		return true
	}
}

func isEventStream(contentType string) bool {
	media, _, _ := strings.Cut(contentType, ";")
	return strings.EqualFold(strings.TrimSpace(media), router.MIMETextEventStream)
}

func acceptsGzip(accept string) bool {
	wildcard := false
	for part := range strings.SplitSeq(accept, ",") {
		token, params, _ := strings.Cut(part, ";")
		switch token = strings.TrimSpace(token); {
		case strings.EqualFold(token, "gzip"), strings.EqualFold(token, "x-gzip"):
			return encodingWanted(params)
		case token == "*":
			wildcard = encodingWanted(params)
		}
	}
	return wildcard
}

func encodingWanted(params string) bool {
	for p := range strings.SplitSeq(params, ";") {
		k, v, ok := strings.Cut(p, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return err == nil && q > 0
	}
	return true
}
