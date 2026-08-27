package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
)

func TestHTTPErrorIsMatchesOnTheStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"the sentinel itself", ErrNotFound, true},
		{"a copy with another message", ErrNotFound.WithMessage("no user 9"), true},
		{"a fresh error with the same status", NewHTTPError(http.StatusNotFound), true},
		{"wrapped by fmt", fmt.Errorf("load user: %w", ErrNotFound), true},
		{"another status", ErrForbidden, false},
		{"a plain error", errors.New("boom"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := errors.Is(tc.err, ErrNotFound); got != tc.want {
				t.Errorf("errors.Is(%v, ErrNotFound) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestHTTPErrorKeepsTheCause(t *testing.T) {
	cause := errors.New("connection refused")
	err := ErrServiceUnavailable.WithError(cause)

	if !errors.Is(err, cause) {
		t.Error("the cause is not reachable through Unwrap")
	}
	// A copy leaves the sentinel untouched.
	if ErrServiceUnavailable.Err != nil {
		t.Error("WithError changed the sentinel")
	}
}

func TestStatusOf(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{nil, http.StatusOK},
		{ErrGone, http.StatusGone},
		{fmt.Errorf("wrapped: %w", ErrTooManyRequests), http.StatusTooManyRequests},
		{errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range tests {
		if got := StatusOf(tc.err); got != tc.want {
			t.Errorf("StatusOf(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

// levelRecorder records the level of every log record it handles.
type levelRecorder struct {
	slog.Handler
	levels []slog.Level
}

func (h *levelRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (h *levelRecorder) Handle(_ context.Context, r slog.Record) error {
	h.levels = append(h.levels, r.Level)
	return nil
}

// A client that goes away mid-response is not a server fault, so it must not
// fire the 5xx alerts of a streamed page.
func TestDefaultErrorHandlerLogsAClientDisconnectAtDebug(t *testing.T) {
	tests := []struct {
		name   string
		cancel bool
		err    error
		want   slog.Level
	}{
		{"a real server fault", false, errors.New("write: broken pipe"), slog.LevelError},
		{"the client cancelled", true, errors.New("write: broken pipe"), slog.LevelDebug},
		{"the error is the cancellation", false, context.Canceled, slog.LevelDebug},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &levelRecorder{Handler: slog.Default().Handler()}
			old := slog.Default()
			slog.SetDefault(slog.New(rec))
			defer slog.SetDefault(old)

			r := newTestRouter()
			r.GET("/", func(c *tctx) error {
				if tc.cancel {
					ctx, cancel := context.WithCancel(c.Request().Context())
					c.SetRequest(c.Request().WithContext(ctx))
					cancel()
				}
				return tc.err
			})
			do(r, http.MethodGet, "/")

			if len(rec.levels) != 1 {
				t.Fatalf("logged %d records, want 1", len(rec.levels))
			}
			if rec.levels[0] != tc.want {
				t.Errorf("level = %v, want %v", rec.levels[0], tc.want)
			}
		})
	}
}

// TestDefaultErrorHandlerAnswersHTMXWithHTML pins the one exception to the
// content negotiation: htmx sends "Accept: */*" and swaps what it receives
// into a page, so a JSON error would land there as text.
func TestDefaultErrorHandlerAnswersHTMXWithHTML(t *testing.T) {
	r := newTestRouter()
	r.GET("/boom", func(*tctx) error {
		return ErrForbidden.WithMessage("no <b>entry</b>")
	})

	tests := []struct {
		name        string
		headers     map[string]string
		contentType string
		body        string
	}{
		{
			name:        "a service gets JSON",
			contentType: MIMEApplicationJSONCharsetUTF8,
			body:        `{"status":403,"error":"no <b>entry</b>"}`,
		},
		{
			name:        "a browser gets text",
			headers:     map[string]string{HeaderAccept: "text/html"},
			contentType: MIMETextPlainCharsetUTF8,
			body:        "no <b>entry</b>",
		},
		{
			name:        "htmx gets escaped HTML",
			headers:     map[string]string{HeaderAccept: "*/*", HeaderHXRequest: "true"},
			contentType: MIMETextHTMLCharsetUTF8,
			body:        "no &lt;b&gt;entry&lt;/b&gt;",
		},
		{
			name: "an htmx request that asks for JSON still gets JSON",
			headers: map[string]string{
				HeaderAccept:    MIMEApplicationJSON,
				HeaderHXRequest: "true",
			},
			contentType: MIMEApplicationJSONCharsetUTF8,
			body:        `{"status":403,"error":"no <b>entry</b>"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := hxDo(r, http.MethodGet, "/boom", tc.headers)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
			if got := rec.Header().Get(HeaderContentType); got != tc.contentType {
				t.Errorf("%s = %q, want %q", HeaderContentType, got, tc.contentType)
			}
			if got := rec.Body.String(); got != tc.body {
				t.Errorf("body = %q, want %q", got, tc.body)
			}
			// Two answers for one URL, so a shared cache has to keep them
			// apart.
			if got := rec.Header().Get(HeaderVary); got != HeaderHXRequest {
				t.Errorf("%s = %q, want %q", HeaderVary, got, HeaderHXRequest)
			}
		})
	}
}
