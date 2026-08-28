package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

// codedError is the error of a package that names its own status without
// importing the router.
type codedError struct{ status int }

func (e *codedError) Error() string { return "the connection string is wrong" }

func (e *codedError) StatusCode() int { return e.status }

func TestStatusOfReadsAStatusCoder(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"the error itself", &codedError{http.StatusPaymentRequired}, http.StatusPaymentRequired},
		{"wrapped by fmt", fmt.Errorf("charge: %w", &codedError{http.StatusConflict}), http.StatusConflict},
		{"a status of zero", &codedError{0}, http.StatusInternalServerError},
		{"an HTTPError wins", ErrGone.WithError(&codedError{http.StatusConflict}), http.StatusGone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StatusOf(tc.err); got != tc.want {
				t.Errorf("StatusOf(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// A StatusCoder contributes its status, never its text: only an HTTPError
// carries a message that is meant for the client.
func TestErrorHandlerHidesTheMessageOfAStatusCoder(t *testing.T) {
	captureLogs(t)

	r := newTestRouter()
	r.GET("/", func(*tctx) error { return &codedError{http.StatusPaymentRequired} })

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusPaymentRequired {
		t.Errorf("status = %d, want 402", rec.Code)
	}
	if got, want := rec.Body.String(), `{"status":402,"error":"Payment Required"}`; got != want {
		t.Errorf("body = %q, want %q; the status is the client's, the message is not", got, want)
	}
}

func TestResolveStatus(t *testing.T) {
	committed := func(status int) *Response {
		res := &Response{ResponseWriter: httptest.NewRecorder()}
		res.WriteHeader(status)
		return res
	}

	tests := []struct {
		name string
		res  *Response
		err  error
		want int
	}{
		{"the handler wrote one", committed(http.StatusCreated), nil, http.StatusCreated},
		{"the handler wrote one and failed after", committed(http.StatusOK), ErrConflict, http.StatusOK},
		{"the error decides", &Response{}, ErrNotFound, http.StatusNotFound},
		{"an internal error", &Response{}, errors.New("boom"), http.StatusInternalServerError},
		{"neither", &Response{}, nil, http.StatusOK},
		{"no response at all", nil, ErrForbidden, http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveStatus(tc.res, tc.err); got != tc.want {
				t.Errorf("ResolveStatus = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFieldErrorCarriesTheField(t *testing.T) {
	email := FieldError{Field: "email", Message: "is not an address"}
	age := FieldError{Field: "age", Message: "is not a number"}

	joined := errors.Join(email, age)
	for _, want := range []string{"email: is not an address", "age: is not a number"} {
		if !strings.Contains(joined.Error(), want) {
			t.Errorf("joined = %q, want it to name %q", joined, want)
		}
	}
	if got, ok := errors.AsType[FieldError](joined); !ok || got != email {
		t.Errorf("AsType = %v/%v, want the first field error", got, ok)
	}
}

func TestFieldErrorsReachTheBody(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(*tctx) error {
		return ErrUnprocessableEntity.WithDetails([]FieldError{
			{Field: "email", Message: "is not an address"},
		})
	})

	rec := do(r, http.MethodPost, "/users")
	want := `{"status":422,"error":"Unprocessable Entity","details":[{"field":"email","message":"is not an address"}]}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestPanicErrorCarriesTheValueAndTheStack(t *testing.T) {
	err := PanicError("boom")

	if !errors.Is(err, ErrInternalServerError) {
		t.Error("the error is not a 500")
	}
	pv, ok := errors.AsType[*PanicValue](err)
	if !ok {
		t.Fatal("the internal cause carries no PanicValue")
	}
	if pv.Value != "boom" {
		t.Errorf("Value = %v, want the panic value", pv.Value)
	}
	if !strings.Contains(string(pv.Stack), "goroutine") {
		t.Errorf("Stack = %q, want the stack of the goroutine", pv.Stack)
	}
	if got := err.Error(); !strings.Contains(got, "panic: boom") {
		t.Errorf("Error() = %q, want it to name the panic", got)
	}

	// A handler that panics with an error keeps it reachable.
	sentinel := errors.New("no rows")
	if !errors.Is(PanicError(sentinel), sentinel) {
		t.Error("the panic value is not reachable through Unwrap")
	}
	if pv, ok := errors.AsType[*PanicValue](PanicError(sentinel)); !ok || pv.Err != sentinel {
		t.Error("Err is not the panic value")
	}
}

func TestPanicErrorSizeCapsTheStack(t *testing.T) {
	tests := []struct {
		name string
		size int
		max  int
	}{
		{"a small buffer", 64, 64},
		{"zero uses the default", 0, DefaultStackSize},
		{"a negative size uses the default", -1, DefaultStackSize},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pv, ok := errors.AsType[*PanicValue](PanicErrorSize("boom", tc.size))
			if !ok {
				t.Fatal("the internal cause carries no PanicValue")
			}
			if len(pv.Stack) == 0 {
				t.Error("the stack is empty")
			}
			if len(pv.Stack) > tc.max {
				t.Errorf("the stack is %d bytes, want at most %d", len(pv.Stack), tc.max)
			}
		})
	}
}

func TestErrorHandlerWithoutTheCauseMatchesTheDefault(t *testing.T) {
	captureLogs(t)

	failing := func(*tctx) error {
		return ErrBadRequest.WithMessage("check the payload").WithError(errors.New("sql: no rows"))
	}

	byDefault := newTestRouter()
	byDefault.GET("/", failing)

	explicit := newTestRouter()
	explicit.ErrorHandler(ErrorHandler[*tctx](false))
	explicit.GET("/", failing)

	want := do(byDefault, http.MethodGet, "/")
	got := do(explicit, http.MethodGet, "/")

	if got.Code != want.Code {
		t.Errorf("status = %d, want %d", got.Code, want.Code)
	}
	if got.Body.String() != want.Body.String() {
		t.Errorf("body = %q, want %q", got.Body.String(), want.Body.String())
	}
	if got.Header().Get(HeaderContentType) != want.Header().Get(HeaderContentType) {
		t.Errorf("Content-Type = %q, want %q",
			got.Header().Get(HeaderContentType), want.Header().Get(HeaderContentType))
	}
	if strings.Contains(got.Body.String(), "sql: no rows") {
		t.Errorf("body = %q, want no internal cause", got.Body.String())
	}
}

func TestErrorHandlerExposesTheCause(t *testing.T) {
	captureLogs(t)

	r := newTestRouter()
	r.ErrorHandler(ErrorHandler[*tctx](true))
	r.GET("/", func(*tctx) error {
		return ErrBadRequest.WithMessage("check the payload").WithError(errors.New("sql: no rows"))
	})

	tests := []struct {
		name   string
		accept string
		want   string
	}{
		{"JSON", "", `{"status":400,"error":"check the payload","cause":"sql: no rows"}`},
		{"text", MIMETextPlain, "check the payload\n\nsql: no rows"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.accept != "" {
				req.Header.Set(HeaderAccept, tc.accept)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
		})
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
