package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/dmitrymomot/go-router"
)

// DefaultGzipMinLength is the shortest body that [Gzip] compresses. A body
// below it grows by the twenty bytes of the gzip framing and saves nothing.
const DefaultGzipMinLength = 1024

// GzipConfig configures [GzipWithConfig].
type GzipConfig struct {
	// Skip passes a request straight to the next handler when it returns true.
	Skip func(c router.Context) bool

	// Level is the compression level, from [gzip.HuffmanOnly] to
	// [gzip.BestCompression]. Zero uses [gzip.DefaultCompression], and a level
	// outside the range is clamped into it.
	Level int

	// MinLength is the shortest body to compress, in bytes. Zero uses
	// [DefaultGzipMinLength].
	MinLength int
}

// Gzip is [GzipWithConfig] with its default config, which compresses at
// [gzip.DefaultCompression] from [DefaultGzipMinLength] bytes up. It is a
// middleware itself, so it goes into Use without a call:
//
//	r.Use(middleware.Gzip[Ctx])
func Gzip[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return GzipWithConfig[C](GzipConfig{})(next)
}

// GzipWithConfig compresses the response body when the client accepts gzip.
//
// It sends Vary: Accept-Encoding on every response, compressed or not, because
// a cache that stores one answer under a key that does not name the encoding
// serves it to a client that cannot read it.
//
// A body shorter than MinLength goes out as it is. The middleware holds the
// header back until it knows which of the two the body is, which is at once
// for a response that carries a Content-Length, as every [router.Base.Render],
// [router.Base.JSON] and [router.Base.String] does, and after MinLength bytes
// for one that does not.
//
// A handler that flushes is streaming, so a flush starts the compressed stream
// whatever it holds by then and MinLength no longer applies. A stream of
// server-sent events is the exception: a text/event-stream response passes
// through uncompressed, because an event that sits in a compression buffer
// reaches the client at the end of the stream instead of at once.
//
// It leaves a 204, a 304, a 206 and a response that already names a
// Content-Encoding alone, and it skips a HEAD request, whose Content-Length
// answers for a body that this response does not carry.
//
// The wrapper sits outside the [router.Response], so [router.Response.Size]
// counts the compressed bytes that reached the client rather than the bytes
// that the handler wrote. That is the number an access log wants;
// [router.Response.Status] is the status either way.
func GzipWithConfig[C router.Context](cfg GzipConfig) router.Middleware[C] {
	level := gzipLevel(cfg.Level)
	minLength := cfg.MinLength
	if minLength <= 0 {
		minLength = DefaultGzipMinLength
	}
	// One pool per config, because the level belongs to the writer and a
	// writer of the wrong level would come out of a shared one.
	pool := &sync.Pool{New: func() any {
		// The level is clamped, so NewWriterLevel cannot report one it refuses.
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
			if req.Method == http.MethodHead || !acceptsGzip(req.Header.Get(router.HeaderAcceptEncoding)) {
				return next(c)
			}

			w := &gzipWriter{ResponseWriter: res.ResponseWriter, res: res, pool: pool, min: minLength}
			before := res.Size
			res.ResponseWriter = w

			// A panic leaves the stream half written. The deferred half runs
			// either way, so that the response writer of the context is the
			// one the error handler expects.
			ok := false
			defer func() {
				w.finish(ok)
				res.ResponseWriter = w.ResponseWriter
				// The response counted the bytes that the handler wrote. The
				// ones that reached the client are the compressed ones.
				res.Size = before + w.written
			}()

			err := next(c)
			ok = true
			return err
		}
	}
}

// The three states of a [gzipWriter]. It starts undecided, and the first
// answer it has enough to give settles the rest of the response.
const (
	gzipUndecided uint8 = iota
	gzipPlain
	gzipOn
)

// gzipWriter compresses what the handler writes. It sits between the
// [router.Response] and the writer of the server, so the response wrapper
// keeps recording the status and the middleware above it keeps reading one
// writer.
type gzipWriter struct {
	http.ResponseWriter

	// res is the response that this wrapper sits under. A flush reaches the
	// wrapper directly, so the header of a stream that flushes before it
	// writes goes back out through the response.
	res *router.Response

	pool *sync.Pool
	gz   *gzip.Writer

	// buf holds the body until it reaches min, which is what a decision about
	// MinLength needs. It stays nil for a response that names its length.
	buf []byte

	// written is the number of bytes that reached the writer underneath, which
	// is the number that the client received.
	written int64

	min   int
	code  int
	state uint8
}

// Unwrap returns the writer underneath, which is what
// [http.NewResponseController] follows to reach the hijack and the deadline
// methods of the server.
func (w *gzipWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// WriteHeader records the status and settles the encoding as soon as the
// headers answer the question. It holds the status line back while the answer
// depends on the length of a body that has not arrived.
//
// A 101 Switching Protocols settles it the same way [router.Response] does: it
// is the answer to the request, and what follows it belongs to the protocol
// that took the connection over, so it commits the response uncompressed.
func (w *gzipWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		// An informational status leaves the response open, so it goes
		// straight out and decides nothing.
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.code != 0 {
		return
	}
	w.code = code

	h := w.Header()
	if !compressibleStatus(code) ||
		h.Get(router.HeaderContentEncoding) != "" ||
		isEventStream(h.Get(router.HeaderContentType)) {
		//nolint:errcheck // Nothing writes a status line and reads an error.
		w.commit(false)
		return
	}
	if n, err := strconv.ParseInt(h.Get(router.HeaderContentLength), 10, 64); err == nil {
		//nolint:errcheck // Same as above: the body follows, and reports it.
		w.commit(n >= int64(w.min))
	}
}

// Write compresses the body, or holds it back until it is long enough to be
// worth compressing.
func (w *gzipWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.WriteHeader(http.StatusOK)
	}
	switch w.state {
	case gzipOn:
		return w.gz.Write(p)
	case gzipPlain:
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

// Flush sends what the wrapper holds and then what the writer underneath
// holds. It is what keeps a streamed page and a stream of events moving,
// because a wrapper that buffered until the end of the response would hold
// every event of a stream that never ends.
func (w *gzipWriter) Flush() {
	if w.code == 0 {
		// The flush asks for a header that no write has answered yet, so the
		// status goes out now, through the checks that settle the encoding: a
		// stream of events, a body that names an encoding of its own and a 204
		// opt out here as they do on a write.
		if w.res.Status == 0 {
			// Through the response, so its Before hooks run and it records
			// the status. The error handler then finds a committed response
			// and writes no second answer into the stream.
			w.res.WriteHeader(http.StatusOK)
		} else {
			// The response holds a status that never reached this wrapper.
			w.WriteHeader(w.res.Status)
		}
	}
	switch w.state {
	case gzipUndecided:
		// The handler is streaming, so the rest of the body is worth
		// compressing whatever this first part weighs, and holding the header
		// back any longer would leave the client waiting for the answer that
		// the flush asked for.
		//nolint:errcheck // Flush reports nothing, as [http.Flusher] does.
		w.commit(true)
	case gzipOn:
		//nolint:errcheck // Same as above.
		w.gz.Flush()
	}
	//nolint:errcheck // A writer that cannot flush says so and changes nothing.
	http.NewResponseController(w.ResponseWriter).Flush()
}

// commit settles the encoding, writes the status line and sends whatever the
// wrapper held back.
func (w *gzipWriter) commit(compress bool) error {
	code := w.code
	if code == 0 {
		code = http.StatusOK
	}
	// Record what this settles. A write that found no status here would run
	// WriteHeader again, read the Content-Encoding that this call sets and
	// answer the same question a second time, the other way.
	w.code = code

	if !compress {
		w.state = gzipPlain
		w.ResponseWriter.WriteHeader(code)
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
		// The server sniffs the type from the first bytes of the body, which
		// are gzip bytes from here on, so the sniff happens on the real ones.
		h.Set(router.HeaderContentType, http.DetectContentType(w.buf))
	}
	// The length describes the body that the handler wrote, not the one that
	// goes out.
	h.Del(router.HeaderContentLength)
	h.Set(router.HeaderContentEncoding, "gzip")

	w.state = gzipOn
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

// finish ends the response. ok reports that the handler returned rather than
// panicked.
func (w *gzipWriter) finish(ok bool) {
	switch w.state {
	case gzipUndecided:
		if w.code == 0 && len(w.buf) == 0 {
			// Nothing reached the wrapper. A hijacked connection ends here,
			// and writing a status line onto it would be a protocol error.
			return
		}
		// The whole body is shorter than MinLength, so it goes out as it is.
		//nolint:errcheck // The response ends here; nothing reads the error.
		w.commit(false)
	case gzipOn:
		if !ok {
			// A panic left the stream half written. Closing it would hand the
			// client a complete gzip member that holds half a page, so the
			// stream stays open and the writer never goes back to the pool.
			return
		}
		//nolint:errcheck // The response is committed; nothing reads the error.
		w.gz.Close()
		w.pool.Put(w.gz)
		w.gz = nil
	}
}

// gzipSink is the writer that the compressed stream drains into. It counts the
// bytes that reach the client, which the compressed stream alone cannot report.
type gzipSink struct{ w *gzipWriter }

// Write implements [io.Writer].
func (s gzipSink) Write(p []byte) (int, error) {
	n, err := s.w.ResponseWriter.Write(p)
	s.w.written += int64(n)
	return n, err
}

// gzipLevel returns a level that [gzip.NewWriterLevel] accepts. Zero is the
// zero value of the config rather than a level the caller asked for, so it
// reads as the default, and a level outside the range is clamped.
func gzipLevel(level int) int {
	switch {
	case level == 0, level < gzip.HuffmanOnly:
		return gzip.DefaultCompression
	case level > gzip.BestCompression:
		return gzip.BestCompression
	default:
		return level
	}
}

// compressibleStatus reports whether a body of this status is worth
// compressing. A 204 and a 304 carry no body, the range of a 206 names bytes
// of a body that the client already has in another encoding, and what follows
// a 101 is the protocol that took the connection over.
func compressibleStatus(code int) bool {
	switch code {
	case http.StatusSwitchingProtocols,
		http.StatusNoContent, http.StatusNotModified, http.StatusPartialContent:
		return false
	default:
		return true
	}
}

// isEventStream reports whether the media type names a stream of server-sent
// events.
func isEventStream(contentType string) bool {
	media, _, _ := strings.Cut(contentType, ";")
	return strings.EqualFold(strings.TrimSpace(media), router.MIMETextEventStream)
}

// acceptsGzip reports whether the client takes a gzip body. It reads the q
// value of each element, so "gzip;q=0" refuses the encoding, and it takes "*"
// for a client that named no encoding of its own.
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

// encodingWanted reports whether the parameters of an Accept-Encoding element
// leave the encoding acceptable. A q value of zero refuses it, and one that
// does not parse refuses it too.
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
