package router

import (
	"context"
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
			r.ErrorHandler(JSONErrorHandler[*tctx])
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
			r.ErrorHandler(JSONErrorHandler[*tctx])
			r.GET("/", tc.fail)

			rec := do(r, http.MethodGet, "/")
			if body := rec.Body.String(); strings.Contains(body, tc.secret) || strings.Contains(body, "hunter2") {
				t.Errorf("body = %s, want no cause", body)
			}
		})
	}
}

func TestJSONErrorHandlerWithUnencodableDetailsAnswers500(t *testing.T) {
	sink := captureLogs(t)

	r := newTestRouter()
	r.ErrorHandler(JSONErrorHandler[*tctx])
	r.GET("/", func(*tctx) error { return ErrConflict.WithDetails(make(chan int)) })

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusInternalServerError || rec.Body.Len() != 0 {
		t.Errorf("answer = %d %q, want a bare 500", rec.Code, rec.Body.String())
	}
	if len(sink.records) == 0 || sink.records[0].Level != slog.LevelError {
		t.Errorf("records = %v, want an Error first", sink.records)
	}
}

func TestDefaultErrorHandlerHEADKeepsRepresentationHeaders(t *testing.T) {
	r := newTestRouter()
	r.GET("/boom", func(*tctx) error { return ErrForbidden })

	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{"JSON", nil},
		{"text", map[string]string{HeaderAccept: MIMETextPlain}},
		{"HTMX", map[string]string{HeaderAccept: "*/*", HeaderHXRequest: "true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			get := hxDo(r, http.MethodGet, "/boom", tc.headers)
			head := hxDo(r, http.MethodHead, "/boom", tc.headers)

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
