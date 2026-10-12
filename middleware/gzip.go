package middleware

import (
	"cmp"
	"compress/gzip"
	"compress/zlib"
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
// Skip does that better. The deflate encoding takes the same level. MinLength
// is the shortest body worth compressing, and zero takes
// [DefaultGzipMinLength].
//
// ExcludeContentTypes names the media types that go out untouched, because
// they are compressed already. An entry is a type and a subtype, as in
// "application/pdf", or a type and "/*" for every subtype, as in "image/*".
// Case and parameters of the Content-Type of the answer do not matter. A nil
// list takes [DefaultGzipExcludeContentTypes]; a non-nil list replaces it, and
// an empty one compresses every type.
type GzipConfig[C router.Context] struct {
	Skip                func(c C) bool
	Level               int
	MinLength           int
	ExcludeContentTypes []string
}

// DefaultGzipExcludeContentTypes is the list [Gzip] leaves alone when
// GzipConfig.ExcludeContentTypes is nil: the types that are compressed
// already. "image/*" stands for every image except "image/svg+xml", which is
// text and compresses well.
var DefaultGzipExcludeContentTypes = []string{
	"image/*", "video/*", "audio/*", "font/woff", "font/woff2",
	"application/zip", "application/gzip", "application/zstd",
	"application/x-7z-compressed", "application/x-rar-compressed",
	"application/octet-stream", "application/pdf",
}

// Gzip compresses the response for a client that says it takes gzip or
// deflate, and adds Accept-Encoding to Vary. The quality values of the client
// choose between the two, and gzip wins a tie. The deflate encoding is the
// zlib format that RFC 9110 names "deflate", not a bare DEFLATE stream.
// Neither the standard library nor this module has a zstd or a brotli
// encoder, so Gzip offers neither.
//
// A body under [DefaultGzipMinLength], a status with no body, a body that is
// already encoded, a media type that is compressed already (see
// [GzipConfig].ExcludeContentTypes), and an answer whose Cache-Control says
// no-transform all go out untouched.
//
// A compressed answer drops Accept-Ranges, since a range of the compressed
// bytes is not a range of the resource. Its ETag gets the suffix "-gzip", or
// "-deflate", inside the quotes, so "abc" becomes "abc-gzip" and W/"abc"
// becomes W/"abc-gzip": the compressed bytes are a representation of their
// own, with a tag of their own. Gzip drops the suffix of the chosen encoding
// from the tags of If-None-Match and If-Match before the handler reads them,
// so the handler compares its own tags. A 304 carries the tag the client
// holds: the suffixed one when If-None-Match named it, or, with no
// If-None-Match, whenever Cache-Control allows a transform.
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
// GzipWithConfig panics on a Level out of range, on a negative MinLength, and
// on an entry of ExcludeContentTypes that is neither a media type nor a type
// with "/*".
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
	excluded := newContentTypeSet(cfg.ExcludeContentTypes)
	gzipEnc := &encoding{suffix: "-gzip", pool: &sync.Pool{New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, level)
		return w
	}}}
	deflateEnc := &encoding{suffix: "-deflate", pool: &sync.Pool{New: func() any {
		w, _ := zlib.NewWriterLevel(io.Discard, level)
		return w
	}}}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			res := c.Response()
			router.AddVary(res.Header(), router.HeaderAcceptEncoding)

			req := c.Request()
			var enc *encoding
			switch negotiateEncoding(req.Header.Get(router.HeaderAcceptEncoding)) {
			case "gzip":
				enc = gzipEnc
			case "deflate":
				enc = deflateEnc
			default:
				return next(c)
			}

			w := &gzipWriter{
				ResponseWriter: res.ResponseWriter,
				res:            res,
				enc:            enc,
				excluded:       excluded,
				min:            minLength,
				head:           req.Method == http.MethodHead,
			}
			w.revalidating, w.unsuffixed = unsuffixETags(req.Header, router.HeaderIfNoneMatch, enc.suffix)
			unsuffixETags(req.Header, headerIfMatch, enc.suffix)
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

// sniffLength is how much of a body [http.DetectContentType] reads.
const sniffLength = 512

// encoder is the writer of one content-coding: a [gzip.Writer] or a
// [zlib.Writer].
type encoder interface {
	io.WriteCloser
	Flush() error
	Reset(w io.Writer)
}

// encoding is a content-coding that Gzip answers with, and the pool of its
// writers. The token of the coding is the name of the suffix without its dash.
type encoding struct {
	suffix string
	pool   *sync.Pool
}

func (e *encoding) token() string { return e.suffix[1:] }

type gzipWriter struct {
	http.ResponseWriter
	res      *router.Response
	enc      *encoding
	excluded contentTypeSet
	gz       encoder
	buf      []byte
	written  int64
	min      int
	code     int
	state    uint8
	head     bool

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
		_ = w.commit(false, nil) // Nothing writes a status line and reads an error.
		return
	}
	if n, err := strconv.ParseInt(h.Get(router.HeaderContentLength), 10, 64); err == nil {
		if n >= int64(w.min) && h.Get(router.HeaderContentType) == "" {
			// Committing now would set Content-Encoding with nothing buffered
			// to sniff, and net/http stops sniffing once it is set. Wait for
			// the first Write.
			return
		}
		_ = w.commit(n >= int64(w.min), nil) // Same as above: the body follows, and reports it.
	}
}

func (w *gzipWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.state != gzipUndecided {
		return w.emit(p)
	}

	// Nothing is buffered and the write alone reaches the minimum: sniff the
	// write in place and send it on, without a copy.
	if len(w.buf) == 0 && len(p) >= w.min {
		if err := w.commit(true, p[:min(len(p), sniffLength)]); err != nil {
			return 0, err
		}
		return w.emit(p)
	}

	// Buffer no more than the minimum. The write that fills the buffer commits
	// it, and the rest of that write goes straight to the encoder.
	if w.buf == nil {
		w.buf = make([]byte, 0, w.min)
	}
	n := min(len(p), w.min-len(w.buf))
	w.buf = append(w.buf, p[:n]...)
	if len(w.buf) < w.min {
		return len(p), nil
	}
	if err := w.commit(true, nil); err != nil {
		return n, err
	}
	if n == len(p) {
		return n, nil
	}
	m, err := w.emit(p[n:])
	return n + m, err
}

// WriteString is [gzipWriter.Write] for a string, so that [router.Response]
// does not convert one that is not buffered or compressed.
func (w *gzipWriter) WriteString(s string) (int, error) {
	if w.code == 0 {
		w.WriteHeader(http.StatusOK)
	}
	switch {
	case w.state != gzipUndecided && w.head:
		return len(s), nil
	case w.state == gzipPlain:
		n, err := io.WriteString(w.ResponseWriter, s)
		w.written += int64(n)
		return n, err
	case w.state == gzipOn:
		return w.writeEncoded(s)
	case len(w.buf) == 0 && len(s) >= w.min:
		if err := w.commit(true, []byte(s[:min(len(s), sniffLength)])); err != nil {
			return 0, err
		}
		return w.WriteString(s)
	case len(w.buf)+len(s) < w.min:
	case w.state == gzipUndecided && len(w.buf)+len(s) < w.min:
		if w.buf == nil {
			w.buf = make([]byte, 0, w.min)
		}
		w.buf = append(w.buf, s...)
		return len(s), nil
	}
	return w.Write([]byte(s))
}

// writeEncoded feeds the encoder a string through a small pooled buffer, since
// an encoder takes bytes and a conversion would copy the whole string.
func (w *gzipWriter) writeEncoded(s string) (int, error) {
	bp := scratchPool.Get().(*[]byte)
	defer scratchPool.Put(bp)
	total := 0
	for len(s) > 0 {
		n := copy(*bp, s)
		m, err := w.gz.Write((*bp)[:n])
		total += m
		if err != nil {
			return total, err
		}
		s = s[n:]
	}
	return total, nil
}

var scratchPool = sync.Pool{New: func() any {
	b := make([]byte, 4096)
	return &b
}}

// emit sends p to the encoder, or to the client when the answer goes out plain.
// The state is decided.
func (w *gzipWriter) emit(p []byte) (int, error) {
	if w.head {
		return len(p), nil
	}
	if w.state == gzipOn {
		return w.gz.Write(p)
	}
	n, err := w.ResponseWriter.Write(p)
	w.written += int64(n)
	return n, err
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
		_ = w.commit(true, nil) // Flush reports nothing, as [http.Flusher] does.
	case gzipOn:
		if w.gz != nil {
			_ = w.gz.Flush() // Same as above.
		}
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush() // A writer that cannot flush says so and changes nothing.
}

// commit sends the status and decides the answer. sniff is the start of the
// body when it is not in the buffer, and the type of an answer without a
// Content-Type comes from it, or else from the buffer.
func (w *gzipWriter) commit(compress bool, sniff []byte) error {
	code := w.code
	if code == 0 {
		code = http.StatusOK
	}
	w.code = code

	h := w.Header()
	if compress {
		if sniff == nil {
			sniff = w.buf[:min(len(w.buf), sniffLength)]
		}
		if h.Get(router.HeaderContentType) == "" && len(sniff) > 0 {
			h.Set(router.HeaderContentType, http.DetectContentType(sniff))
		}
		compress = !w.excluded.has(h.Get(router.HeaderContentType))
	}

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

	h.Del(router.HeaderContentLength)
	h.Del(headerAcceptRanges)
	if etag := h.Get(router.HeaderETag); etag != "" {
		h.Set(router.HeaderETag, suffixETag(etag, w.enc.suffix))
	}
	h.Set(router.HeaderContentEncoding, w.enc.token())

	w.state = gzipOn
	if w.head {
		w.ResponseWriter.WriteHeader(code)
		w.buf = w.buf[:0]
		return nil
	}
	w.gz = w.enc.pool.Get().(encoder)
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
		_ = w.commit(false, nil) // The response ends here; nothing reads the error.
	case gzipOn:
		if !ok || w.head {
			return
		}
		_ = w.gz.Close() // The response is committed; nothing reads the error.
		w.gz.Reset(io.Discard)
		w.enc.pool.Put(w.gz)
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
		h.Set(router.HeaderETag, suffixETag(etag, w.enc.suffix))
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

// suffixETag puts the suffix inside the quotes of an entity tag. A
// value that is not a quoted tag stays as it is.
func suffixETag(etag, suffix string) string {
	tag := strings.TrimPrefix(etag, "W/")
	if len(tag) < 2 || tag[0] != '"' || tag[len(tag)-1] != '"' {
		return etag
	}
	return etag[:len(etag)-1] + suffix + `"`
}

// unsuffixETags drops the suffix from each entity tag of the header
// field name. It reports whether the field is present, and the opaque tags,
// without W/, that lost the suffix. A field it cannot parse stays as it is.
func unsuffixETags(h http.Header, name, suffix string) (present bool, unsuffixed []string) {
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
			if plain, ok := strings.CutSuffix(opaque, suffix+`"`); ok {
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

// negotiateEncoding picks the coding for an Accept-Encoding value: "gzip",
// "deflate", or "" when the client takes neither. The higher quality wins and
// gzip wins a tie. A coding the header names takes its own quality, and "*"
// stands for the ones it does not name.
func negotiateEncoding(accept string) string {
	gz, df, star := -1.0, -1.0, -1.0
	for part := range strings.SplitSeq(accept, ",") {
		token, params, _ := strings.Cut(part, ";")
		switch token = strings.TrimSpace(token); {
		case strings.EqualFold(token, "gzip"), strings.EqualFold(token, "x-gzip"):
			if gz < 0 {
				gz = encodingQuality(params)
			}
		case strings.EqualFold(token, "deflate"):
			if df < 0 {
				df = encodingQuality(params)
			}
		case token == "*":
			if star < 0 {
				star = encodingQuality(params)
			}
		}
	}
	if gz < 0 {
		gz = star
	}
	if df < 0 {
		df = star
	}
	switch {
	case gz > 0 && gz >= df:
		return "gzip"
	case df > 0:
		return "deflate"
	}
	return ""
}

// encodingQuality reads the q parameter, and reports 1 when there is none and 0
// when it does not parse.
func encodingQuality(params string) float64 {
	for p := range strings.SplitSeq(params, ";") {
		k, v, ok := strings.Cut(p, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0
		}
		return q
	}
	return 1
}

// contentTypeSet matches the media type of a Content-Type value against exact
// "type/subtype" entries and "type/*" wildcards.
type contentTypeSet struct {
	exact    map[string]bool
	wildcard map[string]bool
	except   map[string]bool
}

// newContentTypeSet builds the set for a configured list. A nil list takes
// [DefaultGzipExcludeContentTypes], with "image/svg+xml" left out of its
// "image/*". It panics on an entry that is not a media type.
func newContentTypeSet(list []string) contentTypeSet {
	var set contentTypeSet
	if list == nil {
		list = DefaultGzipExcludeContentTypes
		set.except = map[string]bool{"image/svg+xml": true}
	}
	set.exact = make(map[string]bool, len(list))
	set.wildcard = make(map[string]bool)
	for _, entry := range list {
		typ, sub, ok := strings.Cut(strings.ToLower(entry), "/")
		if !ok || typ == "" || sub == "" || typ == "*" ||
			strings.ContainsAny(entry, " \t;,") || strings.Contains(sub, "/") ||
			sub != "*" && strings.Contains(sub, "*") {
			panic("middleware: GzipConfig.ExcludeContentTypes got " + strconv.Quote(entry) +
				`, which is not a media type; write a type and a subtype, as in "application/pdf", ` +
				`or a type with "/*", as in "image/*"`)
		}
		if sub == "*" {
			set.wildcard[typ] = true
		} else {
			set.exact[typ+"/"+sub] = true
		}
	}
	return set
}

// has reports whether the Content-Type value names a type in the set.
func (s contentTypeSet) has(contentType string) bool {
	media, _, _ := strings.Cut(contentType, ";")
	media = strings.ToLower(strings.TrimSpace(media))
	if media == "" || s.except[media] {
		return false
	}
	if s.exact[media] {
		return true
	}
	typ, _, _ := strings.Cut(media, "/")
	return s.wildcard[typ]
}
