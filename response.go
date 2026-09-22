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
	// committing is up while the callbacks of Before run, so a write from
	// inside one does not run them again.
	committing bool
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
//
// A callback that writes commits the response on the spot with the status
// being written and the header as it stands, and the callbacks do not run
// again.
func (r *Response) WriteHeader(code int) {
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		r.ResponseWriter.WriteHeader(code)
		return
	}
	if r.Committed {
		r.dropStatus(code)
		return
	}
	if r.committing {
		// A callback wrote. The status on its way out wins over the one
		// this write asks for.
		if code != r.Status {
			r.dropStatus(code)
		}
		r.ResponseWriter.WriteHeader(r.Status)
		r.Committed = true
		return
	}
	r.Status = code
	r.committing = true
	for _, fn := range r.before {
		fn()
	}
	r.committing = false
	if !r.Committed {
		r.ResponseWriter.WriteHeader(code)
		r.Committed = true
	}
}

func (r *Response) dropStatus(code int) {
	if l := slog.Default(); l.Enabled(context.Background(), slog.LevelDebug) {
		l.Debug("router: the response is already committed",
			slog.Int("dropped", code), slog.Int("status", r.Status))
	}
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

// Flush is [Response.FlushError] without the error, for [http.Flusher].
func (r *Response) Flush() {
	//nolint:errcheck // Flush mirrors http.Flusher, which reports no error.
	r.FlushError()
}

// FlushError sends what is buffered to the client, committing the response
// with a 200 when no status went out yet. It reports the error of the writer
// underneath, or [http.ErrNotSupported] when that writer cannot flush; the
// response is committed either way. [http.NewResponseController] calls it.
func (r *Response) FlushError() error {
	if !r.Committed {
		r.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(r.ResponseWriter).Flush()
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

// Recorded is what [Response.Capture] saw go out.
//
//betteralign:check
type Recorded struct {
	// Header is the header that went out with the status, after the callbacks
	// of [Response.Before] ran. It is nil when no status went out.
	Header http.Header
	// Body holds the body up to the limit of the capture.
	Body []byte
	// Status is 0 when no status went out while the capture ran. A 1xx and a
	// hijack record nothing.
	Status int
	// Truncated reports a body that ran past the limit.
	Truncated bool
}

// Capture records the answer from here on, and the client still gets all of
// it. It keeps the status, the header that goes out with it, and the first
// limit bytes of the body. stop ends the recording and reports it, and a later
// call reports the same. A middleware uses it to store an answer and replay
// it.
//
// Capture puts a writer of its own in place of ResponseWriter, and stop puts
// the one beneath back, unless another writer took its place since. That
// writer implements Unwrap, so Flush, Hijack, the deadlines of
// [http.NewResponseController] and the close after a body over the limit all
// still reach net/http.
//
// A middleware in front that swaps the writer and puts its own back on the way
// out, such as Gzip or HTMXRedirect of package middleware, also takes the
// capture out of the chain. Call stop before handing back to it.
//
// Called after the header went out, Capture starts from that status and
// header.
//
// Capture panics on a negative limit.
func (r *Response) Capture(limit int) (stop func() Recorded) {
	if limit < 0 {
		panic("router: Response.Capture needs a limit of zero or more")
	}
	w := &captureWriter{ResponseWriter: r.ResponseWriter, res: r, limit: limit}
	if r.Committed && r.Status != http.StatusSwitchingProtocols {
		w.status, w.header = r.Status, r.Header().Clone()
	}
	r.ResponseWriter = w
	return w.stop
}

// captureWriter has no Flush or Hijack of its own: Response reaches them
// through Unwrap, and so does http.NewResponseController.
type captureWriter struct {
	http.ResponseWriter
	res       *Response
	header    http.Header
	body      []byte
	status    int
	limit     int
	truncated bool
	stopped   bool
}

func (w *captureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *captureWriter) WriteHeader(code int) {
	if !w.stopped && w.status == 0 && code >= http.StatusOK {
		w.status, w.header = code, w.Header().Clone()
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write records what the handler meant to send, so a write the client did not
// take is still recorded.
func (w *captureWriter) Write(p []byte) (int, error) {
	captureBytes(w, p)
	return w.ResponseWriter.Write(p)
}

func (w *captureWriter) WriteString(s string) (int, error) {
	captureBytes(w, s)
	return io.WriteString(w.ResponseWriter, s)
}

// ReadFrom gives up sendfile while it records, because the bytes have to pass
// through here.
func (w *captureWriter) ReadFrom(src io.Reader) (int64, error) {
	if w.stopped {
		if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
			return rf.ReadFrom(src)
		}
	}
	return io.Copy(struct{ io.Writer }{w}, src)
}

func captureBytes[T string | []byte](w *captureWriter, p T) {
	if w.stopped {
		return
	}
	if w.status == 0 {
		w.status, w.header = http.StatusOK, w.Header().Clone()
	}
	room := w.limit - len(w.body)
	if len(p) > room {
		p, w.truncated = p[:room], true
	}
	w.body = append(w.body, p...)
}

func (w *captureWriter) stop() Recorded {
	if !w.stopped {
		w.stopped = true
		if w.res.ResponseWriter == w {
			w.res.ResponseWriter = w.ResponseWriter
		}
	}
	return Recorded{Header: w.header, Body: w.body, Status: w.status, Truncated: w.truncated}
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
