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

const DefaultGzipMinLength = 1024

type GzipConfig struct {
	Skip      func(c router.Context) bool
	Level     int
	MinLength int
}

func Gzip[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return GzipWithConfig[C](GzipConfig{})(next)
}

func GzipWithConfig[C router.Context](cfg GzipConfig) router.Middleware[C] {
	level := gzipLevel(cfg.Level)
	minLength := cfg.MinLength
	if minLength <= 0 {
		minLength = DefaultGzipMinLength
	}
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
			if req.Method == http.MethodHead || !acceptsGzip(req.Header.Get(router.HeaderAcceptEncoding)) {
				return next(c)
			}

			w := &gzipWriter{ResponseWriter: res.ResponseWriter, res: res, pool: pool, min: minLength}
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
	res *router.Response

	pool    *sync.Pool
	gz      *gzip.Writer
	buf     []byte
	written int64

	min   int
	code  int
	state uint8
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
		//nolint:errcheck // Same as above.
		w.gz.Flush()
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

func (w *gzipWriter) finish(ok bool) {
	switch w.state {
	case gzipUndecided:
		if w.code == 0 && len(w.buf) == 0 {
			return
		}
		//nolint:errcheck // The response ends here; nothing reads the error.
		w.commit(false)
	case gzipOn:
		if !ok {
			return
		}
		//nolint:errcheck // The response is committed; nothing reads the error.
		w.gz.Close()
		w.pool.Put(w.gz)
		w.gz = nil
	}
}

type gzipSink struct{ w *gzipWriter }

func (s gzipSink) Write(p []byte) (int, error) {
	n, err := s.w.ResponseWriter.Write(p)
	s.w.written += int64(n)
	return n, err
}

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
