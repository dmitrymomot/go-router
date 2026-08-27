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
