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

// Response wraps the [http.ResponseWriter] of the request and records what went
// out. It is what [Base.Response] reports.
//
// Status and Size hold what the handler wrote, and Committed says whether the
// header is already out. A middleware reads them after the chain returns.
//
//betteralign:check
type Response struct {
	http.ResponseWriter
	before    []func()
	Status    int
	Size      int64
	Committed bool
}

// Unwrap reports the writer underneath, which lets
// [http.NewResponseController] reach the features of net/http through this
// wrapper.
func (r *Response) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Before registers fn to run just before the header goes out, which is the
// last moment a header can still be set. Callbacks run in the order they were
// added.
//
// Before panics if fn is nil.
func (r *Response) Before(fn func()) {
	if fn == nil {
		panic("router: Response.Before needs a callback")
	}
	r.before = append(r.before, fn)
}

// WriteHeader writes the status and commits the response, after it runs the
// callbacks of [Response.Before]. A 1xx other than 101 passes through as an
// informational response and commits nothing. A second call is dropped and
// logged at debug level.
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

// Write writes b, committing the response with a 200 when no status went out
// yet, and adds to Size.
func (r *Response) Write(b []byte) (int, error) {
	if !r.Committed {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.Size += int64(n)
	return n, err
}

// WriteString writes s, committing the response with a 200 when no status went
// out yet, and adds to Size.
func (r *Response) WriteString(s string) (int, error) {
	if !r.Committed {
		r.WriteHeader(http.StatusOK)
	}
	n, err := io.WriteString(r.ResponseWriter, s)
	r.Size += int64(n)
	return n, err
}

// Flush sends what is buffered to the client, committing the response with a
// 200 when no status went out yet. A writer that cannot flush is left alone.
func (r *Response) Flush() {
	if !r.Committed {
		r.WriteHeader(http.StatusOK)
	}
	//nolint:errcheck // Flush mirrors http.Flusher, which reports no error.
	http.NewResponseController(r.ResponseWriter).Flush()
}

// ReadFrom copies src to the client, committing the response with a 200 when
// no status went out yet, and adds to Size. It hands the copy to the writer
// underneath when that writer can take it, so a file can go out through
// sendfile.
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

// Hijack takes the connection. The response counts as committed afterwards:
// the caller owns the wire, and anything the router writes on top of that draws
// "http: response.WriteHeader on hijacked connection" from net/http. Status is
// recorded as 101, which is what a hijack is nearly always for, so an observer
// does not report a 200 or a 500 for a connection that carried neither.
func (r *Response) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(r.ResponseWriter).Hijack()
	if err != nil {
		return conn, rw, err
	}
	if !r.Committed {
		r.Status, r.Committed = http.StatusSwitchingProtocols, true
	}
	return conn, rw, nil
}

const unwrapLimit = 16

// UnwrapResponse digs a [Response] out of w, through any number of wrappers
// that implement Unwrap. A middleware of net/http uses it to reach the status
// and the size. ok is false when no Response is in the chain.
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
