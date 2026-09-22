package middleware

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/dmitrymomot/go-router"
)

// MinDurationConfig configures [MinDurationWithConfig]. Duration is the floor,
// and it is required.
type MinDurationConfig[C router.Context] struct {
	Skip     func(c C) bool
	Duration time.Duration
}

// MinDuration makes every answer of the routes it covers take at least d,
// counted from the moment the middleware runs. A sign-in form that mails a
// link uses it so that an address with an account and one without answer in
// the same time.
//
// The hold sits in a writer of its own, which holds back the first byte, the
// header included, until the floor passes. A handler that returns without
// writing waits before it hands back, so an answer from the error handler or
// the recovery waits as well.
//
// The hold ends at once when the request context it found ends: the client
// went away, or a [Timeout] in front of it ran out. A Timeout behind it does
// not shorten the floor.
//
// The floor only hides work that ends inside d, so pick a d above the slowest
// path. A middleware in front of it that refuses, such as [KeyAuth], [CSRF] or
// [RateLimit], answers without the hold, so wrap every branch that depends on
// the account. A held request keeps its goroutine for d: put RateLimit in
// front, and keep the WriteTimeout of the server well above d.
//
// A 1xx informational answer is not held. A hijack waits for the floor before
// it takes the connection.
//
// See Order in the package doc for where it goes.
//
// MinDuration panics on a d of zero or less.
func MinDuration[C router.Context](d time.Duration) router.Middleware[C] {
	return MinDurationWithConfig(MinDurationConfig[C]{Duration: d})
}

// MinDurationWithConfig is [MinDuration] with a configuration.
//
// MinDurationWithConfig panics on a Duration of zero or less.
func MinDurationWithConfig[C router.Context](cfg MinDurationConfig[C]) router.Middleware[C] {
	if cfg.Duration <= 0 {
		panic("middleware: MinDurationWithConfig needs a Duration above zero")
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			// The context found here is kept on purpose: a Timeout further in
			// swaps in a shorter one that is still in place when the header
			// goes out.
			res := c.Response()
			w := &holdWriter{
				ResponseWriter: res.ResponseWriter,
				ctx:            c.Request().Context(),
				until:          time.Now().Add(cfg.Duration),
			}
			res.ResponseWriter = w
			defer func() {
				res.ResponseWriter = w.ResponseWriter
				if !res.Committed {
					holdUntil(w.ctx, w.until)
				}
			}()

			return next(c)
		}
	}
}

// holdWriter holds back the first thing that reaches the client until the
// floor passes, then lets everything through.
type holdWriter struct {
	http.ResponseWriter
	ctx   context.Context
	until time.Time
	held  bool
}

func (w *holdWriter) hold() {
	if !w.held {
		w.held = true
		holdUntil(w.ctx, w.until)
	}
}

func (w *holdWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *holdWriter) WriteHeader(code int) {
	if code < 100 || code >= 200 || code == http.StatusSwitchingProtocols {
		w.hold()
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *holdWriter) Write(b []byte) (int, error) {
	w.hold()
	return w.ResponseWriter.Write(b)
}

func (w *holdWriter) WriteString(s string) (int, error) {
	w.hold()
	return io.WriteString(w.ResponseWriter, s)
}

// ReadFrom keeps sendfile: io.Copy hands the copy to the writer beneath when
// it can take it.
func (w *holdWriter) ReadFrom(src io.Reader) (int64, error) {
	w.hold()
	return io.Copy(w.ResponseWriter, src)
}

func (w *holdWriter) Flush() {
	//nolint:errcheck // Flush mirrors http.Flusher, which reports no error.
	w.FlushError()
}

func (w *holdWriter) FlushError() error {
	w.hold()
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *holdWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hold()
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func holdUntil(ctx context.Context, until time.Time) {
	wait := time.Until(until)
	if wait <= 0 {
		return
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
