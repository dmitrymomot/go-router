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

// Response wraps an [http.ResponseWriter] and records the status code and the
// number of bytes that the handler wrote. Middleware reads those fields after
// the handler returns.
//
// Response implements Unwrap, so [http.NewResponseController] reaches the
// hijack and deadline methods of the writer underneath.
//
// betteralign:check
//
// A Response lives inside every [Base], so its layout is worth keeping tight.
type Response struct {
	http.ResponseWriter

	before []func()

	// Status is the status code that the handler wrote. It is 0 until the
	// handler commits the response.
	Status int

	// Size is the number of body bytes that the handler wrote.
	Size int64

	// Committed reports whether the header is already written.
	Committed bool
}

// Unwrap returns the writer underneath, for [http.NewResponseController].
func (r *Response) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Before registers a function that runs immediately before the header is
// written. Use it to set a header that depends on the status code.
func (r *Response) Before(fn func()) { r.before = append(r.before, fn) }

// WriteHeader writes the status line.
//
// An informational status, from 100 to 199, goes straight to the client and
// leaves the response open, so a 103 Early Hints does not swallow the final
// status. It runs no hook, because the hooks belong to the status that answers
// the request.
//
// A second call to a committed response is a no-op that logs the dropped code
// at debug level, which is what names the handler that wrote a body and then
// returned an error.
func (r *Response) WriteHeader(code int) {
	if code >= 100 && code < 200 {
		r.ResponseWriter.WriteHeader(code)
		return
	}
	if r.Committed {
		// The record costs an allocation to build, so ask the handler first.
		if l := slog.Default(); l.Enabled(context.Background(), slog.LevelDebug) {
			l.Debug("router: the response is already committed",
				slog.Int("dropped", code), slog.Int("status", r.Status))
		}
		return
	}
	// The status comes first, so that a hook reads the one that goes out.
	r.Status = code
	for _, fn := range r.before {
		fn()
	}
	r.ResponseWriter.WriteHeader(code)
	r.Committed = true
}

// Write writes the body and commits the response with status 200 if the
// handler did not set a status.
func (r *Response) Write(b []byte) (int, error) {
	if !r.Committed {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.Size += int64(n)
	return n, err
}

// WriteString writes a string body.
func (r *Response) WriteString(s string) (int, error) {
	if !r.Committed {
		r.WriteHeader(http.StatusOK)
	}
	n, err := io.WriteString(r.ResponseWriter, s)
	r.Size += int64(n)
	return n, err
}

// Flush sends any buffered data to the client.
func (r *Response) Flush() {
	//nolint:errcheck // Flush mirrors http.Flusher, which reports no error.
	http.NewResponseController(r.ResponseWriter).Flush()
}

// Hijack takes the connection away from the server, so that the caller speaks
// its own protocol on it. A WebSocket library that asserts [http.Hijacker]
// against the response writer finds it here.
//
// It reaches the connection through [http.NewResponseController], so it works
// through any writer that the server put underneath.
func (r *Response) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(r.ResponseWriter).Hijack()
}

// unwrapLimit bounds the walk of [UnwrapResponse]. A chain of wrappers is a
// handful of writers deep, so a longer one is a cycle.
const unwrapLimit = 16

// UnwrapResponse returns the [Response] that w wraps, and reports whether it
// found one. It follows the Unwrap method of every writer in between, in the
// form that [http.NewResponseController] reads.
//
// A wrapper that a handler puts around the response reads the status and the
// size of the request through it:
//
//	if res, ok := router.UnwrapResponse(w); ok && res.Status == http.StatusOK {
//		// ...
//	}
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

// headerContainsToken reports whether the named header holds the token, after
// a case-insensitive comparison of each comma separated element.
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
