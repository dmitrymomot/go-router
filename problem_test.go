package router

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func problemRouter(exposeCause bool, err error) *Router[*tctx] {
	r := newTestRouter()
	r.ErrorHandler(ProblemErrorHandler[*tctx](exposeCause))
	r.GET("/orders/7", func(*tctx) error { return err })
	return r
}

func TestProblemErrorHandlerWritesTheDocument(t *testing.T) {
	captureLogs(t)

	tests := []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{
			"a problem that names every member",
			&ProblemError{
				Type:     "https://example.com/probs/insufficient-funds",
				Title:    "The account holds too little credit",
				Status:   http.StatusConflict,
				Detail:   "the balance is 30 and the transfer asks for 50",
				Instance: "/accounts/7/transfers/12",
			},
			http.StatusConflict,
			`{"type":"https://example.com/probs/insufficient-funds",` +
				`"title":"The account holds too little credit","status":409,` +
				`"detail":"the balance is 30 and the transfer asks for 50",` +
				`"instance":"/accounts/7/transfers/12"}`,
		},
		{
			"a problem without a type",
			&ProblemError{Status: http.StatusConflict, Detail: "the order is already shipped"},
			http.StatusConflict,
			`{"type":"about:blank","title":"Conflict","status":409,"detail":"the order is already shipped"}`,
		},
		{
			"a problem without a status",
			&ProblemError{Title: "Something gave way"},
			http.StatusInternalServerError,
			`{"type":"about:blank","title":"Something gave way","status":500}`,
		},
		{
			"a sentinel error",
			ErrNotFound,
			http.StatusNotFound,
			`{"type":"about:blank","title":"Not Found","status":404}`,
		},
		{
			"an HTTP error with a message",
			ErrBadRequest.WithMessage("check the payload"),
			http.StatusBadRequest,
			`{"type":"about:blank","title":"Bad Request","status":400,"detail":"check the payload"}`,
		},
		{
			"an HTTP error with field errors",
			ErrUnprocessableEntity.WithDetails([]FieldError{{Field: "email", Message: "is not an address"}}),
			http.StatusUnprocessableEntity,
			`{"type":"about:blank","title":"Unprocessable Entity","status":422,` +
				`"errors":[{"field":"email","message":"is not an address"}]}`,
		},
		{
			"an error of another package that names its status",
			&codedError{http.StatusPaymentRequired},
			http.StatusPaymentRequired,
			`{"type":"about:blank","title":"Payment Required","status":402}`,
		},
		{
			"an HTTP error that carries a problem",
			ErrBadRequest.WithError(&ProblemError{Status: http.StatusConflict, Title: "Too little credit"}),
			http.StatusBadRequest,
			`{"type":"about:blank","title":"Bad Request","status":400}`,
		},
		{
			"any other error",
			errors.New("dial tcp 10.0.0.3:5432: connection refused"),
			http.StatusInternalServerError,
			`{"type":"about:blank","title":"Internal Server Error","status":500}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(problemRouter(false, tc.err), http.MethodGet, "/orders/7")

			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if got := StatusOf(tc.err); got != rec.Code {
				t.Errorf("StatusOf reads %d, and the answer is %d; they have to agree", got, rec.Code)
			}
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
			if got := rec.Header().Get(HeaderContentType); got != MIMEApplicationProblemJSON {
				t.Errorf("Content-Type = %q, want %q", got, MIMEApplicationProblemJSON)
			}
		})
	}
}

func TestProblemErrorHandlerAnswersEveryClient(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/orders/7", nil)
	req.Header.Set(HeaderAccept, MIMETextHTML)
	rec := doReq(problemRouter(false, ErrNotFound), req)

	if got := rec.Header().Get(HeaderContentType); got != MIMEApplicationProblemJSON {
		t.Errorf("Content-Type = %q, want %q", got, MIMEApplicationProblemJSON)
	}
	if got := rec.Body.String(); !strings.Contains(got, `"status":404`) {
		t.Errorf("body = %q, want a problem document", got)
	}
}

func TestProblemErrorHandlerHidesTheInternalMessage(t *testing.T) {
	captureLogs(t)

	tests := []struct {
		name string
		err  error
	}{
		{"a plain error", errors.New("connection string: user=root password=hunter2")},
		{
			"the cause of an HTTP error",
			ErrBadRequest.WithError(errors.New("connection string: user=root password=hunter2")),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := do(problemRouter(false, tc.err), http.MethodGet, "/orders/7").Body.String()
			if strings.Contains(body, "hunter2") {
				t.Errorf("body = %q, want no internal cause", body)
			}
		})
	}
}

func TestProblemErrorHandlerExposesTheCause(t *testing.T) {
	captureLogs(t)

	err := ErrBadRequest.WithMessage("check the payload").WithError(errors.New("sql: no rows"))
	rec := do(problemRouter(true, err), http.MethodGet, "/orders/7")

	want := `{"type":"about:blank","title":"Bad Request","status":400,` +
		`"detail":"check the payload","cause":"sql: no rows"}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestProblemErrorHandlerLogsTheInternalCause(t *testing.T) {
	tests := []struct {
		name   string
		cancel bool
		err    error
		want   []slog.Level
	}{
		{"a server fault", false, errors.New("write: broken pipe"), []slog.Level{slog.LevelError}},
		{"an internal cause", false, ErrBadRequest.WithError(errors.New("sql: no rows")), []slog.Level{slog.LevelWarn}},
		{"a plain client error", false, ErrNotFound, nil},
		{"a problem that a handler described", false, &ProblemError{Status: http.StatusConflict}, nil},
		{"a problem that is a server fault", false, &ProblemError{Status: http.StatusBadGateway}, []slog.Level{slog.LevelError}},
		{"the client cancelled", true, errors.New("write: broken pipe"), []slog.Level{slog.LevelDebug}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &levelRecorder{Handler: slog.Default().Handler()}
			old := slog.Default()
			slog.SetDefault(slog.New(rec))
			defer slog.SetDefault(old)

			r := newTestRouter()
			r.ErrorHandler(ProblemErrorHandler[*tctx](false))
			r.GET("/orders/7", func(c *tctx) error {
				if tc.cancel {
					ctx, cancel := context.WithCancel(c.Request().Context())
					c.SetRequest(c.Request().WithContext(ctx))
					cancel()
				}
				return tc.err
			})
			do(r, http.MethodGet, "/orders/7")

			if len(rec.levels) != len(tc.want) {
				t.Fatalf("logged %v, want %v", rec.levels, tc.want)
			}
			for i, level := range rec.levels {
				if level != tc.want[i] {
					t.Errorf("level = %v, want %v", level, tc.want[i])
				}
			}
		})
	}
}

func TestProblemErrorHandlerSkipsTheLogOfADescribedProblem(t *testing.T) {
	tests := []struct {
		name    string
		handler ErrorHandlerFunc[*tctx]
		want    []slog.Level
	}{
		{"the problem handler", ProblemErrorHandler[*tctx](false), nil},
		{"the default handler", DefaultErrorHandler[*tctx], []slog.Level{slog.LevelWarn}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &levelRecorder{Handler: slog.Default().Handler()}
			old := slog.Default()
			slog.SetDefault(slog.New(rec))
			defer slog.SetDefault(old)

			r := newTestRouter()
			r.ErrorHandler(tc.handler)
			r.GET("/orders/7", func(*tctx) error {
				return &ProblemError{Status: http.StatusConflict, Title: "Too little credit"}
			})
			got := do(r, http.MethodGet, "/orders/7")

			if got.Code != http.StatusConflict {
				t.Errorf("status = %d, want %d", got.Code, http.StatusConflict)
			}
			if !slices.Equal(rec.levels, tc.want) {
				t.Errorf("logged %v, want %v", rec.levels, tc.want)
			}
		})
	}
}

func TestProblemErrorHandlerSkipsACommittedResponse(t *testing.T) {
	captureLogs(t)

	r := newTestRouter()
	r.ErrorHandler(ProblemErrorHandler[*tctx](false))
	r.GET("/orders/7", func(c *tctx) error {
		if err := c.String(http.StatusOK, "half a page"); err != nil {
			return err
		}
		return errors.New("the rest of it failed")
	})

	rec := do(r, http.MethodGet, "/orders/7")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "half a page" {
		t.Errorf("body = %q, want %q", got, "half a page")
	}
}

func TestProblemErrorHandlerHEADWritesNoBody(t *testing.T) {
	r := newTestRouter()
	r.ErrorHandler(ProblemErrorHandler[*tctx](false))
	r.GET("/orders/7", func(*tctx) error { return ErrNotFound })

	rec := do(r, http.MethodHead, "/orders/7")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	if got := rec.Header().Get(HeaderContentType); got != MIMEApplicationProblemJSON {
		t.Errorf("Content-Type = %q, want %q", got, MIMEApplicationProblemJSON)
	}
}

func TestProblemErrorHandlerIgnoresANilError(t *testing.T) {
	b := NewBase(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	writeProblem(b, nil, false)

	if b.Response().Committed {
		t.Error("the handler wrote a response for a nil error")
	}
}

func TestProblemErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		err  *ProblemError
		want string
	}{
		{
			"a title and a detail",
			&ProblemError{Title: "Too little credit", Status: 409, Detail: "the balance is 30"},
			"409 Too little credit: the balance is 30",
		},
		{"a title alone", &ProblemError{Title: "Too little credit", Status: 409}, "409 Too little credit"},
		{"no title", &ProblemError{Status: 404}, "404 Not Found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
			if got := tc.err.StatusCode(); got != tc.err.Status {
				t.Errorf("StatusCode() = %d, want %d", got, tc.err.Status)
			}
		})
	}
}

func TestProblemErrorReachesTheDefaultHandler(t *testing.T) {
	captureLogs(t)

	r := newTestRouter()
	r.GET("/orders/7", func(*tctx) error {
		return &ProblemError{Status: http.StatusConflict, Detail: "the order is already shipped"}
	})

	rec := do(r, http.MethodGet, "/orders/7")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if got := rec.Body.String(); got != `{"status":409,"error":"Conflict"}` {
		t.Errorf("body = %q", got)
	}
	if strings.Contains(rec.Body.String(), "already shipped") {
		t.Errorf("body = %q, want no detail from a handler that reads none", rec.Body.String())
	}
}
