package router

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// betteralign:check
type Response struct {
	http.ResponseWriter

	before    []func()
	Status    int
	Size      int64
	Committed bool
}

func (r *Response) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *Response) Before(fn func()) {
	if fn == nil {
		panic("router: Response.Before needs a callback")
	}
	r.before = append(r.before, fn)
}

func (r *Response) WriteHeader(code int) {
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		r.ResponseWriter.WriteHeader(code)
		return
	}
	if r.Committed {
		if l := slog.Default(); l.Enabled(context.Background(), slog.LevelDebug) {
			l.Debug("router: the response is already committed",
				slog.Int("dropped", code), slog.Int("status", r.Status))
		}
		return
	}
	r.Status = code
	for _, fn := range r.before {
		fn()
	}
	r.ResponseWriter.WriteHeader(code)
	r.Committed = true
}

func (r *Response) Write(b []byte) (int, error) {
	if !r.Committed {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.Size += int64(n)
	return n, err
}

func (r *Response) WriteString(s string) (int, error) {
	if !r.Committed {
		r.WriteHeader(http.StatusOK)
	}
	n, err := io.WriteString(r.ResponseWriter, s)
	r.Size += int64(n)
	return n, err
}

func (r *Response) Flush() {
	if !r.Committed {
		r.WriteHeader(http.StatusOK)
	}
	//nolint:errcheck // Flush mirrors http.Flusher, which reports no error.
	http.NewResponseController(r.ResponseWriter).Flush()
}

func (r *Response) ReadFrom(src io.Reader) (int64, error) {
	if !r.Committed {
		r.WriteHeader(http.StatusOK)
	}
	var n int64
	var err error
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		n, err = rf.ReadFrom(src)
	} else {
		n, err = io.Copy(r.ResponseWriter, src)
	}
	r.Size += n
	return n, err
}

func (r *Response) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(r.ResponseWriter).Hijack()
}

const unwrapLimit = 16

func UnwrapResponse(w http.ResponseWriter) (*Response, bool) {
	for range unwrapLimit {
		switch v := w.(type) {
		case *Response:
			return v, true
		case interface{ Unwrap() http.ResponseWriter }:
			w = v.Unwrap()
		default:
			return nil, false
		}
	}
	return nil, false
}

func headerContainsToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for part := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}
