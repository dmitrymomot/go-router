package router

import (
	"io"
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

// WriteHeader writes the status line. A second call is a no-op.
func (r *Response) WriteHeader(code int) {
	if r.Committed {
		return
	}
	for _, fn := range r.before {
		fn()
	}
	r.Status = code
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
