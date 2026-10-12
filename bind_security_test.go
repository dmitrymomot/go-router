package router

import (
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

type dialingText struct{}

func (*dialingText) UnmarshalText([]byte) error {
	return errors.New("dial tcp 10.0.3.7:5432: connect: connection refused")
}

// A field that does not parse answers a fixed phrase: no input, no Go type and
// no text of an UnmarshalText reach the client, even for a long value. The
// detail stays in the cause, with the input cut.
func TestFieldErrorsKeepTheInputAndTheCauseFromTheClient(t *testing.T) {
	type input struct {
		Age   int           `query:"age"`
		Ratio float64       `query:"ratio"`
		On    bool          `query:"on"`
		TTL   time.Duration `query:"ttl"`
		Since time.Time     `query:"since"`
		Day   time.Time     `query:"day" format:"2006-01-02"`
		Code  dialingText   `query:"code"`
		Small uint8         `query:"small"`
	}
	var bindErr error
	r := newTestRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.GET("/in", func(c *tctx) error {
		_, bindErr = c.BindQuery[input]()
		return bindErr
	})

	long := strings.Repeat("9", 200<<10)
	q := url.Values{
		"age": {long}, "ratio": {"x"}, "on": {"maybe"}, "ttl": {"soon"}, "since": {"yesterday"},
		"day": {"tomorrow"}, "code": {"x"}, "small": {"-1"},
	}
	rec := do(r, http.MethodGet, "/in?"+q.Encode())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if rec.Body.Len() > 1<<10 {
		t.Errorf("body is %d bytes, want it short", rec.Body.Len())
	}
	body := strings.ReplaceAll(rec.Body.String(), "integer", "")
	for _, leak := range []string{"dial tcp", "10.0.3.7", "999", "int", "float64", "time.", "yesterday", "maybe"} {
		if strings.Contains(body, leak) {
			t.Errorf("body repeats %q: %s", leak, rec.Body)
		}
	}
	want := map[string]string{
		"age": "is not a valid integer", "ratio": "is not a valid number", "on": "is not a valid boolean",
		"ttl": "is not a valid duration", "since": "is not a valid time", "day": "is not a valid time",
		"code": "is not a valid value", "small": "is not a valid integer",
	}
	got := map[string]string{}
	for _, f := range FieldErrorsOf(bindErr) {
		got[f.Field] = f.Message
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}

	cause := errors.Unwrap(bindErr).Error()
	if !strings.Contains(cause, "dial tcp 10.0.3.7:5432") {
		t.Errorf("the cause lost the error of UnmarshalText: %s", cause)
	}
	if len(cause) > 2<<10 {
		t.Errorf("the cause is %d bytes, want the input cut", len(cause))
	}
	if !strings.Contains(cause, `"`+strings.Repeat("9", maxEchoBytes)+`…"`) {
		t.Errorf("the cause does not hold the input cut to %d bytes: %s", maxEchoBytes, cause)
	}
}

func TestQueryAsAnswersAFixedPhrase(t *testing.T) {
	r := newTestRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.GET("/in", func(c *tctx) error {
		_, err := c.QueryAs[int]("age")
		return err
	})
	r.GET("/all", func(c *tctx) error {
		_, err := c.QueryAllAs[float64]("ratio")
		return err
	})
	for _, tt := range []struct{ target, want string }{
		{"/in?age=" + strings.Repeat("x", 4096), `query parameter "age" is not a valid integer`},
		{"/all?ratio=1&ratio=abc", `query parameter "ratio" is not a valid number`},
	} {
		rec := do(r, http.MethodGet, tt.target)
		if rec.Code != http.StatusBadRequest || !strings.HasPrefix(rec.Body.String(), tt.want) {
			t.Errorf("%.12s: %d %q, want 400 %q", tt.target, rec.Code, rec.Body.String(), tt.want)
		}
		if strings.Contains(rec.Body.String(), "xxx") || strings.Contains(rec.Body.String(), "abc") {
			t.Errorf("%.12s: the body repeats the input: %q", tt.target, rec.Body.String())
		}
	}
}

func TestQueryAsOfAnUnsupportedTypeIsAnInternalError(t *testing.T) {
	r := newTestRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.GET("/in", func(c *tctx) error {
		_, err := c.QueryAs[chan int]("c")
		return err
	})
	if code := do(r, http.MethodGet, "/in?c=1").Code; code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", code)
	}
}

// A member name comes from the client, so a long one is cut in the field.
func TestBindJSONCutsALongMemberName(t *testing.T) {
	r := newTestRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.POST("/in", func(c *tctx) error {
		_, err := c.BindJSON[createUser](json.RejectUnknownMembers(true))
		return err
	})
	rec := post(r, "/in", MIMEApplicationJSON, `{"`+strings.Repeat("k", 100<<10)+`":1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if rec.Body.Len() > 1<<10 {
		t.Errorf("body is %d bytes, want the member name cut", rec.Body.Len())
	}
}

func TestClipCutsAtARuneBoundary(t *testing.T) {
	s := strings.Repeat("a", maxEchoBytes-1) + "é" + "tail"
	got := clip(s)
	if want := strings.Repeat("a", maxEchoBytes-1) + "…"; got != want {
		t.Errorf("clip = %q, want %q", got, want)
	}
	if got := clip("short"); got != "short" {
		t.Errorf("clip(short) = %q", got)
	}
}

// BindJSON refuses a body whose Content-Type names another type, so a
// text/plain form posted from another site cannot reach a JSON endpoint.
func TestBindJSONChecksTheContentType(t *testing.T) {
	r := newTestRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.POST("/in", func(c *tctx) error {
		in, err := c.BindJSON[createUser]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, in.Name)
	})
	for _, tt := range []struct {
		contentType string
		code        int
	}{
		{"", http.StatusOK},
		{MIMEApplicationJSON, http.StatusOK},
		{"application/json; charset=utf-8", http.StatusOK},
		{"application/json; charset=UTF-8", http.StatusOK},
		{"Application/JSON", http.StatusOK},
		{"application/vnd.api+json", http.StatusOK},
		{"text/plain", http.StatusUnsupportedMediaType},
		{"text/plain; charset=utf-8", http.StatusUnsupportedMediaType},
		{MIMEApplicationForm, http.StatusUnsupportedMediaType},
		{"application/json; charset=latin1", http.StatusUnsupportedMediaType},
		{"application/json; charset=utf-16", http.StatusUnsupportedMediaType},
		{"application/json; charset", http.StatusUnsupportedMediaType},
	} {
		req := httptest.NewRequest(http.MethodPost, "/in", strings.NewReader(`{"name":"ann"}`))
		if tt.contentType != "" {
			req.Header.Set(HeaderContentType, tt.contentType)
		}
		if rec := doReq(r, req); rec.Code != tt.code {
			t.Errorf("Content-Type %q: status = %d, want %d", tt.contentType, rec.Code, tt.code)
		}
	}
}

func TestBindRefusesAJSONBodyInAnotherCharset(t *testing.T) {
	r := newTestRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.POST("/in", func(c *tctx) error {
		_, err := c.Bind[createUser]()
		return err
	})
	for ct, code := range map[string]int{
		"application/json; charset=utf-8":      http.StatusOK,
		"application/json; charset=Utf-8":      http.StatusOK,
		"application/json; charset=iso-8859-1": http.StatusUnsupportedMediaType,
		"application/problem+json; charset=x":  http.StatusUnsupportedMediaType,
	} {
		if got := post(r, "/in", ct, `{"name":"ann"}`).Code; got != code {
			t.Errorf("Content-Type %q: status = %d, want %d", ct, got, code)
		}
	}
}
