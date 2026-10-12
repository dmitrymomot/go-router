package middleware

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// DefaultETagMaxBytes is the largest body [ETag] buffers when MaxBytes is zero.
const DefaultETagMaxBytes = 1 << 20

// ETagConfig configures [ETagWithConfig]. MaxBytes is the largest body to
// buffer, in bytes, and zero takes [DefaultETagMaxBytes]. A response that grows
// past it goes out untouched.
type ETagConfig[C router.Context] struct {
	Skip     func(c C) bool
	MaxBytes int64
}

// ETag makes a GET or HEAD request conditional for a response the handler
// builds on the fly. It buffers a 200 body, gives it a strong ETag (the
// SHA-256 of the body, cut to 128 bits, in base64url) unless the handler set
// one, and answers 304 with no body when If-None-Match names that tag. The
// comparison is weak, as RFC 9110 asks for If-None-Match.
//
// A response passes through untouched when it is not a 200, when its
// Cache-Control says no-store, when it holds more than [DefaultETagMaxBytes]
// bytes, or when the handler flushes it, which commits it and ends the
// buffering. A handler that sets an ETag itself costs no buffer: the answer is
// 304 or 200 at once.
//
// ETag wraps the response writer, and keeps Unwrap so that
// [http.NewResponseController] still reaches the writer underneath.
//
// See Order in the package doc for where it goes.
func ETag[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return ETagWithConfig(ETagConfig[C]{})(next)
}

// ETagWithConfig is [ETag] with a configuration.
//
// ETagWithConfig panics on a negative MaxBytes.
func ETagWithConfig[C router.Context](cfg ETagConfig[C]) router.Middleware[C] {
	if cfg.MaxBytes < 0 {
		panic(fmt.Sprintf("middleware: ETagConfig.MaxBytes is %d; set a size in bytes, or zero for the default", cfg.MaxBytes))
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = DefaultETagMaxBytes
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			req := c.Request()
			if (req.Method != http.MethodGet && req.Method != http.MethodHead) || skipped(cfg.Skip, c) {
				return next(c)
			}
			res := c.Response()
			w := &etagWriter{
				ResponseWriter: res.ResponseWriter,
				res:            res,
				inm:            req.Header.Values(router.HeaderIfNoneMatch),
				max:            cfg.MaxBytes,
			}
			size, status, committed := res.Size, res.Status, res.Committed
			res.ResponseWriter = w

			ok := false
			defer func() {
				res.ResponseWriter = w.ResponseWriter
				if !ok {
					// A panic: nothing reached the client, so undo what the
					// handler recorded and let Recover answer.
					res.Size, res.Status, res.Committed = size, status, committed
					return
				}
				w.finish()
			}()
			err := next(c)
			if err != nil {
				// The handler failed: let out what it wrote, as it wrote it.
				w.passthrough()
			}
			ok = true
			return err
		}
	}
}

const (
	etagUndecided uint8 = iota
	etagBuffering
	etagPassthrough
	etagDiscard
)

type etagWriter struct {
	http.ResponseWriter
	res   *router.Response
	buf   []byte
	inm   []string
	max   int64
	code  int
	state uint8
}

func (w *etagWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *etagWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.code != 0 {
		return
	}
	w.code = code
	h := w.Header()
	if code != http.StatusOK || noStore(h.Values(router.HeaderCacheControl)) {
		w.passthrough()
		return
	}
	if n, err := strconv.ParseInt(h.Get(router.HeaderContentLength), 10, 64); err == nil && n > w.max {
		w.passthrough()
		return
	}
	if tag := h.Get(router.HeaderETag); tag != "" {
		if etagMatches(w.inm, tag) {
			w.notModified()
			return
		}
		w.passthrough()
		return
	}
	w.state = etagBuffering
}

func (w *etagWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.WriteHeader(http.StatusOK)
	}
	switch w.state {
	case etagDiscard:
		return len(p), nil
	case etagBuffering:
		if int64(len(w.buf))+int64(len(p)) <= w.max {
			w.buf = append(w.buf, p...)
			return len(p), nil
		}
		w.passthrough()
	}
	return w.ResponseWriter.Write(p)
}

func (w *etagWriter) Flush() {
	if w.code == 0 {
		w.WriteHeader(http.StatusOK)
	}
	w.passthrough()
	//nolint:errcheck // Flush reports nothing, as [http.Flusher] does.
	http.NewResponseController(w.ResponseWriter).Flush()
}

// passthrough commits the response as the handler wrote it, buffered body
// included, and sends the rest straight on.
func (w *etagWriter) passthrough() {
	switch w.state {
	case etagPassthrough, etagDiscard:
		return
	}
	buffered := w.state == etagBuffering
	w.state = etagPassthrough
	if w.code == 0 {
		return
	}
	w.ResponseWriter.WriteHeader(w.code)
	if buffered && len(w.buf) > 0 {
		//nolint:errcheck // The client is gone if this fails, and the next write says so.
		w.ResponseWriter.Write(w.buf)
	}
	w.buf = nil
}

func (w *etagWriter) notModified() {
	w.state = etagDiscard
	h := w.Header()
	h.Del(router.HeaderContentLength)
	h.Del(router.HeaderContentType)
	w.ResponseWriter.WriteHeader(http.StatusNotModified)
	w.res.Status = http.StatusNotModified
}

// finish answers for a response that is still buffered.
func (w *etagWriter) finish() {
	if w.state != etagBuffering {
		return
	}
	sum := sha256.Sum256(w.buf)
	tag := `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`
	h := w.Header()
	h.Set(router.HeaderETag, tag)
	if etagMatches(w.inm, tag) {
		w.notModified()
		return
	}
	h.Set(router.HeaderContentLength, strconv.Itoa(len(w.buf)))
	w.passthrough()
}

func noStore(values []string) bool {
	for _, v := range values {
		for d := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(d), "no-store") {
				return true
			}
		}
	}
	return false
}

// etagMatches reports whether tag is in the If-None-Match values, comparing
// weakly: the W/ prefix counts for nothing.
func etagMatches(inm []string, tag string) bool {
	tag = strings.TrimPrefix(tag, "W/")
	for _, v := range inm {
		for candidate := range strings.SplitSeq(v, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "*" || strings.TrimPrefix(candidate, "W/") == tag {
				return true
			}
		}
	}
	return false
}
