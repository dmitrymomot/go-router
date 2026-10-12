package router

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
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
		{"a fresh error with the same status", NewHTTPError(http.StatusNotFound, ""), true},
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
		{fmt.Errorf("read body: %w", &http.MaxBytesError{Limit: 4}), http.StatusRequestEntityTooLarge},
	}
	for _, tc := range tests {
		if got := StatusOf(tc.err); got != tc.want {
			t.Errorf("StatusOf(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

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

// canceledCoder names its own status for a cause that is context.Canceled.
type canceledCoder struct{}

func (canceledCoder) Error() string   { return "the upstream call was canceled" }
func (canceledCoder) StatusCode() int { return http.StatusBadGateway }
func (canceledCoder) Unwrap() error   { return context.Canceled }

func TestStatusOfReportsACancelledRequestAs499(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"context.Canceled", context.Canceled, 499},
		{"wrapped by fmt", fmt.Errorf("load user: %w", context.Canceled), 499},
		{"an HTTPError around it keeps its status", ErrServiceUnavailable.WithError(context.Canceled), http.StatusServiceUnavailable},
		{"a StatusCoder around it keeps its status", canceledCoder{}, http.StatusBadGateway},
		{"a deadline is not a disconnect", context.DeadlineExceeded, http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StatusOf(tc.err); got != tc.want {
				t.Errorf("StatusOf(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestErrorHandlerHidesTheMessageOfAStatusCoder(t *testing.T) {
	captureLogs(t)

	r := newTestRouter()
	r.GET("/", func(*tctx) error { return &codedError{http.StatusPaymentRequired} })

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusPaymentRequired {
		t.Errorf("status = %d, want 402", rec.Code)
	}
	if got, want := rec.Body.String(), "Payment Required"; got != want {
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
		{"the client went away", &Response{}, context.Canceled, 499},
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

func TestHTTPErrorOf(t *testing.T) {
	plain := errors.New("db: connection refused")
	coder := &codedError{http.StatusPaymentRequired}
	bare := &HTTPError{}
	teapot := &HTTPError{Status: http.StatusTeapot}

	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
		wantCause   error
	}{
		{"a sentinel", ErrNotFound, http.StatusNotFound, "Not Found", nil},
		{"wrapped by fmt", fmt.Errorf("load: %w", ErrGone), http.StatusGone, "Gone", nil},
		{"no status and no message", bare, http.StatusInternalServerError, "Internal Server Error", nil},
		{"a status and no message", teapot, http.StatusTeapot, "I'm a teapot", nil},
		{"a StatusCoder", coder, http.StatusPaymentRequired, "Payment Required", coder},
		{"a plain error", plain, http.StatusInternalServerError, "Internal Server Error", plain},
		{"the client went away", context.Canceled, 499, "", context.Canceled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			he := HTTPErrorOf(tc.err)
			if he.Status != tc.wantStatus {
				t.Errorf("Status = %d, want %d", he.Status, tc.wantStatus)
			}
			if he.Message != tc.wantMessage {
				t.Errorf("Message = %q, want %q", he.Message, tc.wantMessage)
			}
			if tc.wantCause != nil && !errors.Is(he.Err, tc.wantCause) {
				t.Errorf("Err = %v, want %v", he.Err, tc.wantCause)
			}
		})
	}

	if HTTPErrorOf(nil) != nil {
		t.Error("HTTPErrorOf(nil) is not nil")
	}
	if got := HTTPErrorOf(fmt.Errorf("load: %w", ErrGone)); got != ErrGone {
		t.Error("a wrapped sentinel came back as a copy, want the sentinel itself")
	}
	if bare.Status != 0 || bare.Message != "" || teapot.Message != "" {
		t.Error("HTTPErrorOf changed the caller's error")
	}
	if allocs := testing.AllocsPerRun(100, func() { _ = HTTPErrorOf(ErrNotFound) }); allocs != 0 {
		t.Errorf("HTTPErrorOf(ErrNotFound) allocates %v times, want 0", allocs)
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
	want := "Unprocessable Entity\nemail: is not an address"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestPanicErrorCarriesTheValueAndTheStack(t *testing.T) {
	err := PanicError("boom", 0)

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
	if got := err.Error(); !strings.Contains(got, "panic: boom") || strings.Contains(got, "goroutine") {
		t.Errorf("Error() = %q, want it to name the panic and leave the stack out", got)
	}

	sentinel := errors.New("no rows")
	if !errors.Is(PanicError(sentinel, 0), sentinel) {
		t.Error("the panic value is not reachable through Unwrap")
	}
	if pv, ok := errors.AsType[*PanicValue](PanicError(sentinel, 0)); !ok || pv.Err != sentinel {
		t.Error("Err is not the panic value")
	}
}

func TestPanicErrorCapsTheStack(t *testing.T) {
	tests := []struct {
		name string
		size int
		min  int
		max  int
	}{
		{"a small buffer", 64, 1, 64},
		{"zero uses the default", 0, 1, DefaultStackSize},
		{"a negative size records no stack", -1, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pv, ok := errors.AsType[*PanicValue](PanicError("boom", tc.size))
			if !ok {
				t.Fatal("the internal cause carries no PanicValue")
			}
			if pv.Value != "boom" {
				t.Errorf("Value = %v, want the panic value", pv.Value)
			}
			if len(pv.Stack) < tc.min {
				t.Errorf("the stack is %d bytes, want at least %d", len(pv.Stack), tc.min)
			}
			if len(pv.Stack) > tc.max {
				t.Errorf("the stack is %d bytes, want at most %d", len(pv.Stack), tc.max)
			}
		})
	}
}

func TestTextErrorHandlerWithoutTheCauseMatchesTheDefault(t *testing.T) {
	captureLogs(t)

	failing := func(*tctx) error {
		return ErrBadRequest.WithMessage("check the payload").WithError(errors.New("sql: no rows"))
	}

	byDefault := newTestRouter()
	byDefault.GET("/", failing)

	explicit := newTestRouter()
	explicit.ErrorHandler(TextErrorHandler[*tctx](false))
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

func TestTextErrorHandlerExposesTheCause(t *testing.T) {
	captureLogs(t)

	r := newTestRouter()
	r.ErrorHandler(TextErrorHandler[*tctx](true))
	r.GET("/", func(*tctx) error {
		return ErrBadRequest.WithMessage("check the payload").WithError(errors.New("sql: no rows"))
	})

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if got, want := rec.Body.String(), "check the payload\n\nsql: no rows"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

type levelRecorder struct {
	slog.Handler
	levels []slog.Level
}

func (h *levelRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (h *levelRecorder) Handle(_ context.Context, r slog.Record) error {
	h.levels = append(h.levels, r.Level)
	return nil
}

func TestEveryErrorHandlerLogsAClientThatWentAwayAtDebug(t *testing.T) {
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
	for _, custom := range []bool{false, true} {
		for _, tc := range tests {
			t.Run(fmt.Sprintf("custom=%v/%s", custom, tc.name), func(t *testing.T) {
				rec := &levelRecorder{Handler: slog.Default().Handler()}
				old := slog.Default()
				slog.SetDefault(slog.New(rec))
				defer slog.SetDefault(old)

				calls := 0
				r := newTestRouter()
				if custom {
					r.ErrorHandler(func(c *tctx, err error) error {
						calls++
						return c.JSON(StatusOf(err), map[string]string{"error": "failed"})
					})
				}
				r.GET("/", func(c *tctx) error {
					if tc.cancel {
						ctx, cancel := context.WithCancel(c.Request().Context())
						c.SetContext(ctx)
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
				if custom && errors.Is(tc.err, context.Canceled) && calls != 0 {
					t.Errorf("the error handler ran %d times for a canceled error, want 0", calls)
				}
			})
		}
	}
}

// errSignIn is a plain error that a custom handler turns into a redirect.
var errSignIn = errors.New("sign in first")

func TestEveryErrorHandlerIsLoggedByThePolicy(t *testing.T) {
	errDenied := errors.New("denied")
	tests := []struct {
		name       string
		err        error
		wantLogged bool
		wantLevel  slog.Level
		wantStatus int64
	}{
		{"a plain error", errors.New("boom"), true, slog.LevelError, http.StatusInternalServerError},
		{"a StatusCoder 404", &codedError{http.StatusNotFound}, true, slog.LevelWarn, http.StatusNotFound},
		{"a bare ErrNotFound", ErrNotFound, false, 0, 0},
		{"a bare ErrInternalServerError", ErrInternalServerError, true, slog.LevelError, http.StatusInternalServerError},
		{"a 4xx with a cause", ErrBadRequest.WithError(errors.New("bad id")), true, slog.LevelWarn, http.StatusBadRequest},
		{"mapped to 403 by the handler", errDenied, true, slog.LevelWarn, http.StatusForbidden},
		{"mapped to a redirect by the handler", errSignIn, false, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sink := captureLogs(t)

			r := newTestRouter()
			r.ErrorHandler(func(c *tctx, err error) error {
				switch {
				case errors.Is(err, errDenied):
					return c.String(http.StatusForbidden, "denied")
				case errors.Is(err, errSignIn):
					return c.Redirect(http.StatusSeeOther, "/login")
				}
				return c.NoContent(StatusOf(err))
			})
			r.GET("/", func(*tctx) error { return tc.err })
			do(r, http.MethodGet, "/")

			if !tc.wantLogged {
				if len(sink.records) != 0 {
					t.Errorf("logged %d records, want none", len(sink.records))
				}
				return
			}
			if len(sink.records) != 1 {
				t.Fatalf("logged %d records, want 1", len(sink.records))
			}
			got := sink.records[0]
			if got.Level != tc.wantLevel {
				t.Errorf("level = %v, want %v", got.Level, tc.wantLevel)
			}
			if status, _ := intAttr(got, "status"); status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
		})
	}
}

func TestHandleErrorOutsideARouter(t *testing.T) {
	captureLogs(t)

	rec := httptest.NewRecorder()
	b := NewBase(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	HandleError(b, ErrNotFound)
	if rec.Code != http.StatusNotFound || rec.Body.String() != "Not Found" {
		t.Errorf("answer = %d %q, want 404 %q", rec.Code, rec.Body.String(), "Not Found")
	}
	if got := rec.Header().Get(HeaderContentType); got != MIMETextPlainCharsetUTF8 {
		t.Errorf("Content-Type = %q, want %q", got, MIMETextPlainCharsetUTF8)
	}

	rec = httptest.NewRecorder()
	b = NewBase(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	HandleError(b, context.Canceled)
	if b.Response().Committed || rec.Body.Len() != 0 {
		t.Errorf("a canceled error wrote %d %q, want nothing", rec.Code, rec.Body.String())
	}
}

type validatedUser struct {
	Name string `json:"name"`
}

func (u validatedUser) Validate() error {
	if u.Name == "" {
		return FieldError{Field: "name", Message: "is required"}
	}
	return nil
}

func TestJSONErrorHandler(t *testing.T) {
	captureLogs(t)

	tests := []struct {
		name     string
		method   string
		handler  HandlerFunc[*tctx]
		body     string
		wantCode int
		wantBody string
	}{
		{
			name:     "a message",
			method:   http.MethodGet,
			handler:  func(*tctx) error { return ErrNotFound.WithMessage("no user 9") },
			wantCode: http.StatusNotFound,
			wantBody: `{"error":{"status":404,"message":"no user 9"}}`,
		},
		{
			name:   "a Bind validation failure",
			method: http.MethodPost,
			handler: func(c *tctx) error {
				_, err := c.Bind[validatedUser]()
				return err
			},
			body:     `{"name":""}`,
			wantCode: http.StatusUnprocessableEntity,
			wantBody: `{"error":{"status":422,"message":"Unprocessable Entity","details":[{"field":"name","message":"is required"}]}}`,
		},
		{
			name:     "a StatusCoder",
			method:   http.MethodGet,
			handler:  func(*tctx) error { return &codedError{http.StatusPaymentRequired} },
			wantCode: http.StatusPaymentRequired,
			wantBody: `{"error":{"status":402,"message":"Payment Required"}}`,
		},
		{
			name:     "a HEAD request",
			method:   http.MethodHead,
			handler:  func(*tctx) error { return ErrForbidden },
			wantCode: http.StatusForbidden,
		},
		{
			name:   "a Content-Type the handler set",
			method: http.MethodGet,
			handler: func(c *tctx) error {
				c.Response().Header().Set(HeaderContentType, MIMETextHTMLCharsetUTF8)
				return ErrConflict
			},
			wantCode: http.StatusConflict,
			wantBody: `{"error":{"status":409,"message":"Conflict"}}`,
		},
		{
			name:   "details that are not field errors",
			method: http.MethodGet,
			handler: func(*tctx) error {
				return ErrTooManyRequests.WithDetails(map[string]int{"retry_after": 30})
			},
			wantCode: http.StatusTooManyRequests,
			wantBody: `{"error":{"status":429,"message":"Too Many Requests","details":{"retry_after":30}}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRouter()
			r.ErrorHandler(JSONErrorHandler[*tctx](false))
			r.Handle(tc.method, "/", tc.handler)

			req := httptest.NewRequest(tc.method, "/", strings.NewReader(tc.body))
			if tc.body != "" {
				req.Header.Set(HeaderContentType, MIMEApplicationJSON)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if got := rec.Body.String(); got != tc.wantBody {
				t.Errorf("body = %s, want %s", got, tc.wantBody)
			}
			if got := rec.Header().Get(HeaderContentType); got != MIMEApplicationJSONCharsetUTF8 {
				t.Errorf("Content-Type = %q, want %q", got, MIMEApplicationJSONCharsetUTF8)
			}
		})
	}
}

// The C08 regression: an API error writer that sent err.Error() leaked the
// cause of every 500 to the client.
func TestJSONErrorHandlerKeepsTheCauseOutOfTheBody(t *testing.T) {
	captureLogs(t)

	for _, tc := range []struct {
		name   string
		fail   HandlerFunc[*tctx]
		secret string
	}{
		{"a plain error", func(*tctx) error { return errors.New("db password=hunter2") }, "hunter2"},
		{"an HTTPError with a cause", func(*tctx) error {
			return ErrBadRequest.WithError(errors.New("db password=hunter2"))
		}, "hunter2"},
		{"a panic", func(*tctx) error { panic("db password=hunter2") }, "panic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRouter()
			r.ErrorHandler(JSONErrorHandler[*tctx](false))
			r.GET("/", tc.fail)

			rec := do(r, http.MethodGet, "/")
			if body := rec.Body.String(); strings.Contains(body, tc.secret) || strings.Contains(body, "hunter2") {
				t.Errorf("body = %s, want no cause", body)
			}
		})
	}
}

func TestJSONErrorHandlerExposesTheCause(t *testing.T) {
	captureLogs(t)

	for _, tc := range []struct {
		name string
		fail HandlerFunc[*tctx]
		want string
	}{
		{"an HTTPError with a cause", func(*tctx) error {
			return ErrBadRequest.WithMessage("check the payload").WithError(errors.New("sql: no rows"))
		}, `{"error":{"status":400,"message":"check the payload","cause":"sql: no rows"}}`},
		{
			"a plain error", func(*tctx) error { return errors.New("sql: no rows") },
			`{"error":{"status":500,"message":"Internal Server Error","cause":"sql: no rows"}}`,
		},
		{
			"no cause", func(*tctx) error { return ErrNotFound },
			`{"error":{"status":404,"message":"Not Found"}}`,
		},
		{
			"a panic", func(*tctx) error { panic("boom") },
			`{"error":{"status":500,"message":"Internal Server Error","cause":"panic: boom"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRouter()
			r.ErrorHandler(JSONErrorHandler[*tctx](true))
			r.GET("/", tc.fail)

			if got := do(r, http.MethodGet, "/").Body.String(); got != tc.want {
				t.Errorf("body = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestJSONErrorHandlerWithUnencodableDetailsAnswers500(t *testing.T) {
	sink := captureLogs(t)

	r := newTestRouter()
	r.ErrorHandler(JSONErrorHandler[*tctx](false))
	r.GET("/", func(*tctx) error { return ErrConflict.WithDetails(make(chan int)) })

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusInternalServerError || rec.Body.Len() != 0 {
		t.Errorf("answer = %d %q, want a bare 500", rec.Code, rec.Body.String())
	}
	if len(sink.records) == 0 || sink.records[0].Level != slog.LevelError {
		t.Errorf("records = %v, want an Error first", sink.records)
	}
}

func TestTextErrorHandlerHEADKeepsRepresentationHeaders(t *testing.T) {
	r := newTestRouter()
	r.GET("/boom", func(*tctx) error { return ErrForbidden })

	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{"JSON", nil},
		{"text", map[string]string{HeaderAccept: MIMETextPlain}},
		{"HTMX", map[string]string{HeaderAccept: "*/*", "Hx-Request": "true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			get := doWithHeaders(r, http.MethodGet, "/boom", tc.headers)
			head := doWithHeaders(r, http.MethodHead, "/boom", tc.headers)

			if head.Code != get.Code {
				t.Errorf("HEAD status = %d, GET status = %d", head.Code, get.Code)
			}
			if got, want := head.Header().Get(HeaderContentType), get.Header().Get(HeaderContentType); got != want {
				t.Errorf("HEAD Content-Type = %q, GET Content-Type = %q", got, want)
			}
			if got, want := head.Header().Values(HeaderVary), get.Header().Values(HeaderVary); !slices.Equal(got, want) {
				t.Errorf("HEAD Vary = %v, GET Vary = %v", got, want)
			}
			if head.Body.Len() != 0 {
				t.Errorf("HEAD body = %q, want empty", head.Body.String())
			}
		})
	}
}

// A client may send Accept more than once, and the error path reads them all.
func TestAcceptsReadsEveryAcceptLine(t *testing.T) {
	r := newTestRouter()
	r.GET("/a", func(c *tctx) error {
		return c.String(http.StatusOK, c.Accepts(MIMETextHTML, MIMEApplicationJSON))
	})

	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	req.Header.Add(HeaderAccept, MIMETextPlain)
	req.Header.Add(HeaderAccept, MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if got := rec.Body.String(); got != MIMEApplicationJSON {
		t.Errorf("Accepts over two Accept lines = %q, want %q", got, MIMEApplicationJSON)
	}
}

// net/http sniffs only when the Content-Type key is absent, not when it is
// present and empty.
func TestBlobWithNoContentTypeLetsTheServerSniff(t *testing.T) {
	r := newTestRouter()
	r.GET("/b", func(c *tctx) error { return c.Blob(http.StatusOK, "", []byte("<html>hi</html>")) })

	rec := do(r, http.MethodGet, "/b")
	if _, ok := rec.Result().Header[HeaderContentType]; ok {
		t.Errorf("Content-Type = %q, want the header left out so the server sniffs",
			rec.Header().Get(HeaderContentType))
	}
}

func TestFieldErrorsOf(t *testing.T) {
	email := FieldError{Field: "email", Message: "is not an address"}
	age := FieldError{Field: "age", Message: "must be 18 or more"}

	tests := []struct {
		name string
		err  error
		want []FieldError
	}{
		{name: "one field error", err: email, want: []FieldError{email}},
		{name: "a pointer to one", err: &email, want: []FieldError{email}},
		{name: "several joined", err: errors.Join(email, age), want: []FieldError{email, age}},
		{
			name: "the details of an HTTPError",
			err:  ErrUnprocessableEntity.WithDetails([]FieldError{email}),
			want: []FieldError{email},
		},
		{name: "a wrapped one", err: fmt.Errorf("read the form: %w", email), want: []FieldError{email}},
		{name: "an error that names no field", err: errors.New("nope")},
		{name: "no error at all"},
		{name: "a typed nil HTTPError", err: (*HTTPError)(nil)},
		{name: "a typed nil FieldError", err: (*FieldError)(nil)},
		{name: "an HTTPError with no fields in its details", err: ErrBadRequest.WithDetails([]FieldError{})},
		{
			name: "an HTTPError whose cause names a field",
			err:  ErrBadRequest.WithDetails("see the cause").WithError(fmt.Errorf("decode: %w", age)),
			want: []FieldError{age},
		},
		{
			name: "details and a cause that hold the same fields",
			err: ErrBadRequest.WithMessage("invalid request").
				WithDetails([]FieldError{email, age}).
				WithError(errors.Join(email, age)),
			want: []FieldError{email, age},
		},
		{
			name: "a field joined with an HTTPError",
			err:  errors.Join(email, ErrUnprocessableEntity.WithDetails([]FieldError{age})),
			want: []FieldError{email, age},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FieldErrorsOf(tt.err); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FieldErrorsOf = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestFieldErrorsOfReturnsACopy(t *testing.T) {
	fields := []FieldError{{Field: "email", Message: "is not an address"}}
	err := ErrUnprocessableEntity.WithDetails(fields)

	got := FieldErrorsOf(err)
	got[0].Message = "changed"

	if fields[0].Message != "is not an address" {
		t.Errorf("Details[0].Message = %q, want it untouched", fields[0].Message)
	}
}

// A handler that returns a typed nil *HTTPError must not crash the error
// pipeline on the way to its 500.
func TestTypedNilHTTPErrorAnswers500(t *testing.T) {
	sink := &recordSink{}
	r := newTestRouter()
	r.Logger(slog.New(sink))
	r.GET("/nil", func(*tctx) error {
		var he *HTTPError
		return he
	})
	if rec := do(r, http.MethodGet, "/nil"); rec.Code != http.StatusInternalServerError {
		t.Errorf("GET /nil = %d, want 500", rec.Code)
	}
	for _, rec := range sink.records {
		rec.Attrs(func(a slog.Attr) bool {
			if err, ok := a.Value.Any().(error); ok {
				if _, ok := errors.AsType[*PanicValue](err); ok {
					t.Errorf("record %q logs a panic: %v", rec.Message, err)
				}
			}
			return true
		})
	}
}

func TestTypedNilHTTPErrorIsNotAnHTTPError(t *testing.T) {
	var he *HTTPError
	err := error(he)

	if got := StatusOf(err); got != http.StatusInternalServerError {
		t.Errorf("StatusOf = %d, want 500", got)
	}
	if got := HTTPErrorOf(err); got == nil || got.Status != http.StatusInternalServerError {
		t.Errorf("HTTPErrorOf = %v, want a 500", got)
	}
	if errors.Is(ErrNotFound, err) {
		t.Error("errors.Is(ErrNotFound, typed nil) = true, want false")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("errors.Is(typed nil, ErrNotFound) = true, want false")
	}
	if errors.Is(err, context.Canceled) {
		t.Error("errors.Is(typed nil, context.Canceled) = true, want false")
	}
	if he.Unwrap() != nil {
		t.Error("Unwrap of a typed nil is not nil")
	}
	_ = he.Error()
}

func TestHTTPErrorIsMatchesADirectTargetOnly(t *testing.T) {
	// A target that wraps an HTTPError is not an HTTPError itself.
	wrapped := fmt.Errorf("load: %w", ErrNotFound)
	if errors.Is(ErrNotFound, wrapped) {
		t.Error("errors.Is(ErrNotFound, wrapped 404) = true, want false")
	}
}

func TestExposedPanicKeepsTheStackInTheLog(t *testing.T) {
	var logged bytes.Buffer
	r := newTestRouter()
	r.Logger(slog.New(slog.NewTextHandler(&logged, nil)))
	r.ErrorHandler(TextErrorHandler[*tctx](true))
	r.GET("/", func(*tctx) error { panic("boom") })

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "goroutine") || !strings.Contains(body, "boom") {
		t.Errorf("body = %q, want the panic value and no stack", body)
	}
	if !strings.Contains(logged.String(), "goroutine") {
		t.Errorf("log = %q, want the stack", logged.String())
	}
}

type joinedNilValidated struct {
	Name string `query:"name"`
}

func (joinedNilValidated) Validate() error {
	return errors.Join((*HTTPError)(nil), ErrConflict.WithMessage("taken"))
}

// A typed nil *HTTPError early in the tree must not hide a real one after it.
func TestATypedNilHTTPErrorDoesNotHideALaterOne(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
	}{
		{"joined", errors.Join((*HTTPError)(nil), ErrNotFound)},
		{"wrapped then joined", errors.Join(fmt.Errorf("lookup: %w", (*HTTPError)(nil)), fmt.Errorf("db: %w", ErrNotFound))},
		{"nested join", fmt.Errorf("outer: %w", errors.Join(errors.New("plain"), errors.Join((*HTTPError)(nil), ErrNotFound)))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := StatusOf(tt.err); got != http.StatusNotFound {
				t.Errorf("StatusOf = %d, want 404", got)
			}
			if he := HTTPErrorOf(tt.err); he.Status != http.StatusNotFound || he.Message != "Not Found" {
				t.Errorf("HTTPErrorOf = %d %q, want 404 %q", he.Status, he.Message, "Not Found")
			}

			sink := &recordSink{}
			r := newTestRouter()
			r.Logger(slog.New(sink))
			r.GET("/x", func(*tctx) error { return tt.err })
			if rec := do(r, http.MethodGet, "/x"); rec.Code != http.StatusNotFound {
				t.Errorf("GET /x = %d, want 404", rec.Code)
			}
			if len(sink.records) != 0 {
				t.Errorf("logged %d records, want none for a 404", len(sink.records))
			}
		})
	}

	t.Run("validate", func(t *testing.T) {
		r := newTestRouter()
		r.GET("/v", func(c *tctx) error {
			_, err := c.BindQuery[joinedNilValidated]()
			return err
		})
		if rec := do(r, http.MethodGet, "/v?name=a"); rec.Code != http.StatusConflict {
			t.Errorf("GET /v = %d %q, want 409", rec.Code, rec.Body.String())
		}
	})
}

func quietRouter() *Router[*tctx] {
	r := newTestRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	return r
}

// dirty sets the headers of a successful response that an error must not keep.
func dirty(c *tctx) {
	h := c.res.Header()
	h.Set(HeaderCacheControl, "public, max-age=31536000")
	h.Set(HeaderETag, `"v1"`)
	h.Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
	h.Set(HeaderContentDisposition, `attachment; filename="a.bin"`)
	h.Set(HeaderContentEncoding, "gzip")
	h.Set(HeaderContentLength, "999")
}

func TestErrorHandlersDropTheHeadersOfTheFailedHandler(t *testing.T) {
	handlers := map[string]ErrorHandlerFunc[*tctx]{
		"text":    TextErrorHandler[*tctx](false),
		"json":    JSONErrorHandler[*tctx](false),
		"problem": ProblemErrorHandler[*tctx](false),
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			r := quietRouter()
			r.ErrorHandler(h)
			r.GET("/", func(c *tctx) error {
				dirty(c)
				return errors.New("boom")
			})
			rec := do(r, http.MethodGet, "/")
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d", rec.Code)
			}
			for _, k := range []string{HeaderCacheControl, HeaderETag, "Last-Modified", HeaderContentDisposition, HeaderContentEncoding} {
				if v := rec.Header().Get(k); v != "" {
					t.Errorf("%s = %q, want it removed", k, v)
				}
			}
			if got := rec.Header().Get(HeaderXContentTypeOptions); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q", got)
			}
			if cl := rec.Header().Get(HeaderContentLength); cl == "999" {
				t.Errorf("Content-Length kept the stale value")
			}
		})
	}
}

func TestBareErrorResponseDropsTheHeadersToo(t *testing.T) {
	r := quietRouter()
	r.ErrorHandler(func(*tctx, error) error { return errors.New("the handler failed") })
	r.GET("/", func(c *tctx) error {
		dirty(c)
		return errors.New("boom")
	})
	rec := do(r, http.MethodGet, "/")
	if rec.Code != 500 || rec.Header().Get(HeaderCacheControl) != "" || rec.Header().Get(HeaderETag) != "" {
		t.Errorf("status %d, headers %v", rec.Code, rec.Header())
	}
}

func TestResetForError(t *testing.T) {
	dropped := []string{HeaderContentLength, HeaderContentDisposition, HeaderContentEncoding, HeaderETag, HeaderLastModified, HeaderCacheControl}
	h := http.Header{}
	for _, k := range dropped {
		h.Set(k, "x")
	}
	h.Set(HeaderContentType, "image/png")
	h.Set("X-Other", "kept")
	ResetForError(h)
	for _, k := range dropped {
		if h.Get(k) != "" {
			t.Errorf("%s kept: %v", k, h)
		}
	}
	if h.Get("X-Other") != "kept" || h.Get(HeaderContentType) != "image/png" {
		t.Errorf("header = %v", h)
	}
	if h.Get(HeaderXContentTypeOptions) != "nosniff" {
		t.Errorf("no nosniff: %v", h)
	}

	empty := http.Header{}
	ResetForError(empty)
	if len(empty) != 1 || empty.Get(HeaderXContentTypeOptions) != "nosniff" {
		t.Errorf("empty header = %v", empty)
	}
}

func TestProblemErrorHandler(t *testing.T) {
	r := quietRouter()
	r.ErrorHandler(ProblemErrorHandler[*tctx](true))
	r.GET("/users/{id}", func(c *tctx) error {
		return ErrNotFound.WithMessage("no user %s", c.Param("id")).WithError(errors.New("sql: no rows"))
	})
	r.POST("/users", func(c *tctx) error {
		return ErrUnprocessableEntity.WithDetails([]FieldError{{Field: "email", Message: "required"}})
	})
	r.PUT("/other", func(c *tctx) error { return ErrConflict.WithDetails(map[string]int{"n": 1}) })

	rec := do(r, http.MethodGet, "/users/9")
	if rec.Code != 404 || rec.Header().Get(HeaderContentType) != "application/problem+json" {
		t.Fatalf("status %d, type %q", rec.Code, rec.Header().Get(HeaderContentType))
	}
	want := `{"type":"about:blank","title":"Not Found","status":404,"detail":"no user 9","instance":"/users/9","cause":"sql: no rows"}`
	if rec.Body.String() != want {
		t.Errorf("body = %s\nwant   %s", rec.Body, want)
	}

	rec = do(r, http.MethodPost, "/users")
	want = `{"type":"about:blank","title":"Unprocessable Entity","status":422,"detail":"Unprocessable Entity","instance":"/users","errors":[{"field":"email","message":"required"}]}`
	if rec.Body.String() != want {
		t.Errorf("body = %s\nwant   %s", rec.Body, want)
	}

	rec = do(r, http.MethodPut, "/other")
	if !strings.Contains(rec.Body.String(), `"details":{"n":1}`) {
		t.Errorf("body = %s", rec.Body)
	}

	rec = do(r, http.MethodHead, "/users/9")
	if rec.Code != 404 || rec.Body.Len() != 0 || rec.Header().Get(HeaderContentLength) == "" {
		t.Errorf("HEAD: status %d, body %q, headers %v", rec.Code, rec.Body, rec.Header())
	}

	var p Problem
	if err := json.Unmarshal(do(r, http.MethodGet, "/users/9").Body.Bytes(), &p); err != nil || p.Status != 404 {
		t.Errorf("round trip: %v %+v", err, p)
	}
}

func TestProblemErrorHandlerHidesTheCause(t *testing.T) {
	r := quietRouter()
	r.ErrorHandler(ProblemErrorHandler[*tctx](false))
	r.GET("/", func(*tctx) error { return ErrBadGateway.WithError(errors.New("secret")) })
	if body := do(r, http.MethodGet, "/").Body.String(); strings.Contains(body, "secret") {
		t.Errorf("body leaks the cause: %s", body)
	}
}

func TestInvalidStatusInAnErrorGivesABare500(t *testing.T) {
	r := quietRouter()
	r.GET("/", func(*tctx) error { return &HTTPError{Status: 1000, Message: "x"} })
	if rec := do(r, http.MethodGet, "/"); rec.Code != 500 {
		t.Errorf("status = %d", rec.Code)
	}
}
