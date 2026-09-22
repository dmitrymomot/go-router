package router

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"uuid"
)

type createUser struct {
	Name    string    `json:"name"`
	Age     int       `json:"age"`
	Admin   bool      `json:"admin"`
	Tags    []string  `json:"tags"`
	Since   time.Time `json:"since"`
	Ignored string    `json:"-"`
}

type formUser struct {
	Name  string        `form:"name"`
	Age   int           `form:"age"`
	Admin bool          `form:"admin"`
	TTL   time.Duration `form:"ttl"`
}

func TestBindJSON(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		in, err := c.Bind[createUser]()
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, in)
	})

	req := httptest.NewRequest(http.MethodPost, "/users",
		strings.NewReader(`{"name":"ann","age":30,"tags":["a","b"]}`))
	req.Header.Set(HeaderContentType, MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"name":"ann"`) {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestBindJSONRejectsMalformedBody(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		_, err := c.Bind[createUser]()
		return err
	})

	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"name":`))
	req.Header.Set(HeaderContentType, MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestBindJSONTellsAnEmptyBodyFromAMalformedOne(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		_, err := c.BindJSON[createUser]()
		return err
	})

	for _, tt := range []struct {
		name  string
		body  string
		empty bool
	}{
		{name: "an empty body", body: "", empty: true},
		{name: "a truncated body", body: `{"name":`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := post(r, "/users", MIMEApplicationJSON, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if got := strings.Contains(rec.Body.String(), "the request body is empty"); got != tt.empty {
				t.Errorf("body = %s, want the empty body message: %v", rec.Body, tt.empty)
			}
			if first, _, _ := strings.Cut(rec.Body.String(), "\n"); !tt.empty && first != "malformed JSON body" {
				t.Errorf("first body line = %q, want exactly %q", first, "malformed JSON body")
			}
		})
	}
}

func TestBindRejectsAnUnknownMediaType(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		_, err := c.Bind[createUser]()
		return err
	})

	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader("x"))
	req.Header.Set(HeaderContentType, "application/xml")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
}

func TestBindBodyLimit(t *testing.T) {
	r := newTestRouter()
	r.MaxBodyBytes(16)
	r.POST("/users", func(c *tctx) error {
		_, err := c.Bind[createUser]()
		return err
	})

	req := httptest.NewRequest(http.MethodPost, "/users",
		strings.NewReader(`{"name":"`+strings.Repeat("x", 100)+`"}`))
	req.Header.Set(HeaderContentType, MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestBindForm(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		in, err := c.BindForm[formUser]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%s/%d/%v/%v", in.Name, in.Age, in.Admin, in.TTL))
	})

	body := url.Values{"name": {"bo"}, "age": {"7"}, "admin": {"true"}, "ttl": {"90s"}}
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body.Encode()))
	req.Header.Set(HeaderContentType, MIMEApplicationForm)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if got, want := rec.Body.String(), "bo/7/true/1m30s"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestBindQuery(t *testing.T) {
	type filter struct {
		Page  int       `query:"page"`
		Limit int       `query:"limit"`
		Sort  []string  `query:"sort"`
		Since time.Time `query:"since"`
	}

	r := newTestRouter()
	r.GET("/search", func(c *tctx) error {
		in, err := c.Bind[filter]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%d/%d/%v/%s",
			in.Page, in.Limit, in.Sort, in.Since.Format(time.RFC3339)))
	})

	rec := do(r, http.MethodGet, "/search?page=2&limit=&sort=a&sort=b&since=2026-01-02T03:04:05Z")
	if got, want := rec.Body.String(), "2/0/[a b]/2026-01-02T03:04:05Z"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestBindQueryReportsAParseError(t *testing.T) {
	type filter struct {
		Page int `query:"page"`
	}
	r := newTestRouter()
	r.GET("/search", func(c *tctx) error {
		_, err := c.Bind[filter]()
		return err
	})

	rec := do(r, http.MethodGet, "/search?page=abc")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "page: ") {
		t.Errorf("body does not name the field: %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cannot parse") {
		t.Errorf("body does not say what went wrong: %q", rec.Body.String())
	}
}

func TestBindQueryLeavesAnOptionalFieldNilForAnEmptyValue(t *testing.T) {
	type filter struct {
		Page *int `query:"page"`
	}
	r := newTestRouter()
	r.GET("/search", func(c *tctx) error {
		in, err := c.BindQuery[filter]()
		if err != nil {
			return err
		}
		if in.Page == nil {
			return c.String(http.StatusOK, "unset")
		}
		return c.String(http.StatusOK, fmt.Sprintf("%d", *in.Page))
	})

	for _, tt := range []struct{ target, want string }{
		{target: "/search", want: "unset"},
		{target: "/search?page=", want: "unset"},
		{target: "/search?page=3", want: "3"},
	} {
		t.Run(tt.target, func(t *testing.T) {
			if got := do(r, http.MethodGet, tt.target).Body.String(); got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBindQueryLeavesAFailedPointerNil(t *testing.T) {
	type filter struct {
		Page *int `query:"page"`
	}
	var got filter
	var gotAs *int
	r := newTestRouter()
	r.GET("/search", func(c *tctx) error {
		var err error
		got, err = c.BindQuery[filter]()
		if err == nil {
			t.Error("BindQuery reported no error for page=abc")
		}
		if gotAs, err = c.QueryAs[*int]("page"); err == nil {
			t.Error("QueryAs reported no error for page=abc")
		}
		return nil
	})

	do(r, http.MethodGet, "/search?page=abc")
	if got.Page != nil {
		t.Errorf("BindQuery Page = %d, want nil", *got.Page)
	}
	if gotAs != nil {
		t.Errorf("QueryAs = %d, want nil", *gotAs)
	}
}

func TestParamAsAndQueryAs(t *testing.T) {
	r := newTestRouter()
	r.GET("/users/{id}", func(c *tctx) error {
		id, err := c.ParamAs[int]("id")
		if err != nil {
			return err
		}
		limit := c.QueryAsDefault("limit", 25)
		return c.String(http.StatusOK, fmt.Sprintf("%d/%d", id, limit))
	})

	if got, want := do(r, http.MethodGet, "/users/9").Body.String(), "9/25"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if got, want := do(r, http.MethodGet, "/users/9?limit=5").Body.String(), "9/5"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if code := do(r, http.MethodGet, "/users/abc").Code; code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
}

// v0.1.0 answered 400 for a path parameter that did not parse, where the
// resource it names does not exist.
func TestParamAsAnswers404ForAMalformedValue(t *testing.T) {
	var seen error
	r := newTestRouter()
	r.ErrorHandler(func(c *tctx, err error) error {
		seen = err
		return DefaultErrorHandler(c, err)
	})
	r.GET("/users/{id}", func(c *tctx) error {
		id, err := c.ParamAs[int]("id")
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%d", id))
	})
	r.GET("/agents/{agent}", func(c *tctx) error {
		id, err := c.ParamAs[uuid.UUID]("agent")
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, id.String())
	})

	rec := do(r, http.MethodGet, "/users/abc")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "Not Found" {
		t.Errorf("body = %q, want the bare status text", got)
	}
	if !errors.Is(seen, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", seen)
	}
	if cause := errors.Unwrap(seen); cause == nil || !strings.Contains(cause.Error(), "route parameter id") {
		t.Errorf("cause = %v, want one that names the parameter", cause)
	}

	const id = "0198c5b6-3f0e-7b3a-9c1d-2f4e6a8b0c1d"
	if rec := do(r, http.MethodGet, "/agents/"+id); rec.Code != http.StatusOK || rec.Body.String() != id {
		t.Errorf("GET /agents/%s = %d %q", id, rec.Code, rec.Body)
	}
	if got := do(r, http.MethodGet, "/agents/new").Code; got != http.StatusNotFound {
		t.Errorf("GET /agents/new = %d, want 404", got)
	}
}

func TestParamAsReportsAMissingNameAs500(t *testing.T) {
	var seen error
	r := newTestRouter()
	r.ErrorHandler(func(c *tctx, err error) error {
		seen = err
		return DefaultErrorHandler(c, err)
	})
	r.GET("/users/{id}", func(c *tctx) error {
		_, err := c.ParamAs[int]("nope")
		return err
	})

	if got := do(r, http.MethodGet, "/users/7").Code; got != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", got)
	}
	if !errors.Is(seen, ErrInternalServerError) {
		t.Errorf("err = %v, want ErrInternalServerError", seen)
	}
	if msg := errors.Unwrap(seen).Error(); !strings.Contains(msg, `"nope"`) || !strings.Contains(msg, `"/users/{id}"`) {
		t.Errorf("cause = %q, want one that names the parameter and the route", msg)
	}
}

func TestParamAsReadsAHostParameter(t *testing.T) {
	r := newTestRouter()
	r.Host("{n}.example.com", func(h *Router[*tctx]) {
		h.GET("/", func(c *tctx) error {
			n, err := c.ParamAs[int]("n")
			if err != nil {
				return err
			}
			return c.String(http.StatusOK, fmt.Sprintf("%d", n))
		})
	})

	if rec := doHost(r, http.MethodGet, "42.example.com", "/"); rec.Code != http.StatusOK || rec.Body.String() != "42" {
		t.Errorf("42.example.com = %d %q, want 200 42", rec.Code, rec.Body)
	}
	if got := doHost(r, http.MethodGet, "x.example.com", "/").Code; got != http.StatusNotFound {
		t.Errorf("x.example.com = %d, want 404", got)
	}
}

func TestDecodeValuesFlattensEmbeddedStructs(t *testing.T) {
	type page struct {
		Offset int `query:"offset"`
	}
	type query struct {
		page
		Term string `query:"q"`
	}

	var got query
	if fields := decodeValues(url.Values{"offset": {"40"}, "q": {"go"}}, reflect.ValueOf(&got).Elem(), "query"); len(fields) != 0 {
		t.Fatalf("fields = %v, want none", fields)
	}
	if got.Offset != 40 || got.Term != "go" {
		t.Errorf("got %+v", got)
	}
}

func TestJSONOptionsApplyToBind(t *testing.T) {
	r := newTestRouter()
	r.JSONOptions(json.RejectUnknownMembers(true))
	r.POST("/users", func(c *tctx) error {
		_, err := c.Bind[createUser]()
		return err
	})

	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"nope":1}`))
	req.Header.Set(HeaderContentType, MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	want := []FieldError{{Field: "nope", Message: "is not a known field"}}
	if got := details(t, rec); !reflect.DeepEqual(got, want) {
		t.Errorf("details = %+v, want %+v", got, want)
	}
}

type rejectedText struct{}

func (*rejectedText) UnmarshalText([]byte) error { return errors.New("the secret reason") }

type mistyped struct {
	Age     int `json:"age"`
	Address struct {
		Zip int `json:"zip"`
	} `json:"address"`
	Items []struct {
		N int `json:"n"`
	} `json:"items"`
	M     map[string]int `json:"m"`
	Small int8           `json:"small"`
	N     int            `json:"n"`
	Code  rejectedText   `json:"code"`
}

func TestBindJSONNamesTheMemberOfAWrongType(t *testing.T) {
	var bindErr error
	r := newTestRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.POST("/in", func(c *tctx) error {
		_, bindErr = c.BindJSON[mistyped]()
		return bindErr
	})

	for _, tt := range []struct {
		body string
		want FieldError
	}{
		{body: `{"age":"x"}`, want: FieldError{Field: "age", Message: "has the wrong JSON type"}},
		{body: `{"address":{"zip":"x"}}`, want: FieldError{Field: "address.zip", Message: "has the wrong JSON type"}},
		{body: `{"items":[{"n":1},{"n":"x"}]}`, want: FieldError{Field: "items[1].n", Message: "has the wrong JSON type"}},
		{body: `{"m":{"a/b":"x"}}`, want: FieldError{Field: "m.a/b", Message: "has the wrong JSON type"}},
		{body: `{"small":300}`, want: FieldError{Field: "small", Message: "is not a valid value"}},
		{body: `{"n":1.5}`, want: FieldError{Field: "n", Message: "is not a valid value"}},
		{body: `{"code":"x"}`, want: FieldError{Field: "code", Message: "is not a valid value"}},
	} {
		t.Run(tt.body, func(t *testing.T) {
			bindErr = nil
			rec := post(r, "/in", MIMEApplicationJSON, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
			body := rec.Body.String()
			if first, _, _ := strings.Cut(body, "\n"); first != "invalid request" {
				t.Errorf("first body line = %q, want %q", first, "invalid request")
			}
			if got := details(t, rec); !reflect.DeepEqual(got, []FieldError{tt.want}) {
				t.Errorf("details = %+v, want %+v", got, tt.want)
			}
			for _, leak := range []string{"json:", "Go ", "the secret reason"} {
				if strings.Contains(body, leak) {
					t.Errorf("body = %q, carries %q", body, leak)
				}
			}
			if _, ok := errors.AsType[*json.SemanticError](bindErr); !ok {
				t.Errorf("error = %v, want a *json.SemanticError in it", bindErr)
			}
		})
	}
}

func TestBindJSONReturnsTheMembersBeforeTheFailure(t *testing.T) {
	var in createUser
	var bindErr error
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		in, bindErr = c.BindJSON[createUser]()
		return bindErr
	})

	post(r, "/users", MIMEApplicationJSON, `{"name":"ann","age":"x","admin":true}`)
	if in.Name != "ann" || in.Admin {
		t.Errorf("in = %+v, want the name and nothing after the age", in)
	}
	want := []FieldError{{Field: "age", Message: "has the wrong JSON type"}}
	if got := FieldErrorsOf(bindErr); !reflect.DeepEqual(got, want) {
		t.Errorf("FieldErrorsOf = %+v, want %+v", got, want)
	}
}

func TestBindJSONKeepsTheParserTextInTheCause(t *testing.T) {
	var bindErr error
	r := newTestRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.POST("/users", func(c *tctx) error {
		_, bindErr = c.BindJSON[createUser]()
		return bindErr
	})
	r.POST("/count", func(c *tctx) error {
		_, bindErr = c.BindJSON[int]()
		return bindErr
	})

	rec := post(r, "/users", MIMEApplicationJSON, `{"name":`)
	if rec.Code != http.StatusBadRequest || rec.Body.String() != "malformed JSON body" {
		t.Errorf("answer = %d %q, want 400 %q", rec.Code, rec.Body.String(), "malformed JSON body")
	}
	if _, ok := errors.AsType[*jsontext.SyntacticError](bindErr); !ok {
		t.Errorf("error = %v, want a *jsontext.SyntacticError in it", bindErr)
	}

	rec = post(r, "/users", MIMEApplicationJSON, `{"name":"a"} x`)
	if rec.Code != http.StatusBadRequest || rec.Body.String() != "malformed JSON body" {
		t.Errorf("trailing garbage: answer = %d %q, want 400 %q", rec.Code, rec.Body.String(), "malformed JSON body")
	}

	rec = post(r, "/users", MIMEApplicationJSON, `[1]`)
	if rec.Code != http.StatusBadRequest || rec.Body.String() != "the request body has the wrong JSON type" {
		t.Errorf("root: answer = %d %q, want 400 %q", rec.Code, rec.Body.String(), "the request body has the wrong JSON type")
	}
	if fields := FieldErrorsOf(bindErr); fields != nil {
		t.Errorf("root: FieldErrorsOf = %+v, want nil", fields)
	}
	if _, ok := errors.AsType[*json.SemanticError](bindErr); !ok {
		t.Errorf("root: error = %v, want a *json.SemanticError in it", bindErr)
	}

	rec = post(r, "/count", MIMEApplicationJSON, `1.5`)
	if rec.Code != http.StatusBadRequest || rec.Body.String() != "the request body is not a valid value" {
		t.Errorf("root value: answer = %d %q, want 400 %q", rec.Code, rec.Body.String(), "the request body is not a valid value")
	}

	dev := newTestRouter()
	dev.Logger(slog.New(slog.DiscardHandler))
	dev.ErrorHandler(ErrorHandler[*tctx](true))
	dev.POST("/users", func(c *tctx) error {
		_, err := c.BindJSON[createUser]()
		return err
	})
	rec = post(dev, "/users", MIMEApplicationJSON, `{"name":`)
	if body := rec.Body.String(); !strings.HasPrefix(body, "malformed JSON body\n\n") || !strings.Contains(body, "jsontext:") {
		t.Errorf("exposed cause: body = %q, want the parser text after the message", body)
	}
}

func TestJSONField(t *testing.T) {
	for _, tt := range []struct{ pointer, want string }{
		{pointer: "", want: ""},
		{pointer: "/a", want: "a"},
		{pointer: "/a/b", want: "a.b"},
		{pointer: "/a/0/b", want: "a[0].b"},
		{pointer: "/0/n", want: "[0].n"},
		{pointer: "/a~1b", want: "a/b"},
		{pointer: "/a~0b", want: "a~b"},
	} {
		if got := jsonField(jsontext.Pointer(tt.pointer)); got != tt.want {
			t.Errorf("jsonField(%q) = %q, want %q", tt.pointer, got, tt.want)
		}
	}
}

func post(h http.Handler, target, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set(HeaderContentType, contentType)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func postForm(h http.Handler, target string, values url.Values) *httptest.ResponseRecorder {
	return post(h, target, MIMEApplicationForm, values.Encode())
}

type upload struct {
	field   string
	name    string
	content string
}

func multipartBody(t *testing.T, values url.Values, files ...upload) (body, contentType string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, vs := range values {
		for _, v := range vs {
			if err := w.WriteField(k, v); err != nil {
				t.Fatalf("write field %s: %v", k, err)
			}
		}
	}
	for _, u := range files {
		fw, err := w.CreateFormFile(u.field, u.name)
		if err != nil {
			t.Fatalf("create file %s: %v", u.field, err)
		}
		if _, err := io.WriteString(fw, u.content); err != nil {
			t.Fatalf("write file %s: %v", u.field, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf.String(), w.FormDataContentType()
}

// details reads the field errors back out of a plain-text error body, which
// carries them one per line after the message as "field: message".
func details(t *testing.T, rec *httptest.ResponseRecorder) []FieldError {
	t.Helper()
	lines := strings.Split(strings.TrimRight(rec.Body.String(), "\n"), "\n")
	var out []FieldError
	for _, line := range lines[1:] {
		field, msg, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		out = append(out, FieldError{Field: field, Message: msg})
	}
	return out
}

func TestBindFormAppliesTheBodyLimit(t *testing.T) {
	r := newTestRouter()
	r.MaxBodyBytes(16)
	r.POST("/users", func(c *tctx) error {
		_, err := c.BindForm[formUser]()
		return err
	})

	rec := postForm(r, "/users", url.Values{"name": {strings.Repeat("x", 100)}})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestBindFormRejectsAMalformedMultipartBody(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		_, err := c.BindForm[formUser]()
		return err
	})

	rec := post(r, "/users", "multipart/form-data; boundary=zz", "not a multipart body")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "malformed form body") {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestBindFormReadsAMultipartBody(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		in, err := c.BindForm[formUser]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%s/%d", in.Name, in.Age))
	})

	body, ct := multipartBody(t, url.Values{"name": {"bo"}, "age": {"7"}})
	if got, want := post(r, "/users", ct, body).Body.String(), "bo/7"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestParseFormReadsTheBodyOnce(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		first, err := c.BindForm[formUser]()
		if err != nil {
			return err
		}
		second, err := c.BindForm[formUser]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%s/%s/%s", first.Name, second.Name, c.FormValue("name")))
	})

	rec := postForm(r, "/users", url.Values{"name": {"bo"}})
	if got, want := rec.Body.String(), "bo/bo/bo"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestFormFileRejectsABodyOverTheLimit(t *testing.T) {
	r := newTestRouter()
	r.MaxBodyBytes(64)
	r.POST("/avatars", func(c *tctx) error {
		f, _, err := c.FormFile("avatar")
		if err != nil {
			return err
		}
		defer f.Close() //nolint:errcheck // The test is done with it.
		return c.String(http.StatusOK, "ok")
	})

	body, ct := multipartBody(t, nil, upload{field: "avatar", name: "a.png", content: strings.Repeat("x", 512)})
	rec := post(r, "/avatars", ct, body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", rec.Code, rec.Body)
	}
}

func TestFormFileReadsTheUpload(t *testing.T) {
	r := newTestRouter()
	r.POST("/avatars", func(c *tctx) error {
		f, h, err := c.FormFile("avatar")
		if err != nil {
			return err
		}
		defer f.Close() //nolint:errcheck // The test is done with it.
		content, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%s/%d/%s", h.Filename, h.Size, content))
	})

	body, ct := multipartBody(t, nil, upload{field: "avatar", name: "a.png", content: "hello"})
	if got, want := post(r, "/avatars", ct, body).Body.String(), "a.png/5/hello"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestFormFileRejectsAMissingField(t *testing.T) {
	r := newTestRouter()
	r.POST("/avatars", func(c *tctx) error {
		_, _, err := c.FormFile("avatar")
		return err
	})

	body, ct := multipartBody(t, url.Values{"name": {"bo"}})
	rec := post(r, "/avatars", ct, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no uploaded file named") {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestFormFilesReturnsEveryFileOfAField(t *testing.T) {
	r := newTestRouter()
	r.POST("/photos", func(c *tctx) error {
		files, err := c.FormFiles("photo")
		if err != nil {
			return err
		}
		names := make([]string, len(files))
		for i, fh := range files {
			names[i] = fh.Filename
		}
		return c.String(http.StatusOK, strings.Join(names, ","))
	})

	body, ct := multipartBody(t, nil,
		upload{field: "photo", name: "a.png", content: "a"},
		upload{field: "photo", name: "b.png", content: "b"},
	)
	if got, want := post(r, "/photos", ct, body).Body.String(), "a.png,b.png"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestFormFilesRejectsAFieldWithoutAFile(t *testing.T) {
	r := newTestRouter()
	r.POST("/photos", func(c *tctx) error {
		_, err := c.FormFiles("photo")
		return err
	})

	body, ct := multipartBody(t, url.Values{"photo": {"not a file"}})
	rec := post(r, "/photos", ct, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestBindFormUsesTheMultipartMemoryOfTheRouter(t *testing.T) {
	for _, tt := range []struct {
		name   string
		memory int64
	}{
		{name: "the default", memory: 0},
		{name: "a limit that spills to disk", memory: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestRouter()
			if tt.memory != 0 {
				r.MaxMultipartMemory(tt.memory)
			}
			r.POST("/avatars", func(c *tctx) error {
				f, _, err := c.FormFile("avatar")
				if err != nil {
					return err
				}
				defer f.Close() //nolint:errcheck // The test is done with it.
				content, err := io.ReadAll(f)
				if err != nil {
					return err
				}
				return c.String(http.StatusOK, fmt.Sprintf("%d", len(content)))
			})

			body, ct := multipartBody(t, nil, upload{field: "avatar", name: "a.png", content: strings.Repeat("x", 2048)})
			if got, want := post(r, "/avatars", ct, body).Body.String(), "2048"; got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
		})
	}
}

func TestMultipartTempFilesGoAwayWithTheRequest(t *testing.T) {
	copyRequest := func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			c.SetRequest(c.Request().WithContext(c.Request().Context()))
			return next(c)
		}
	}
	uploads := func(mw ...Middleware[*tctx]) *Router[*tctx] {
		r := newTestRouter()
		r.MaxMultipartMemory(1)
		r.Use(mw...)
		r.POST("/avatars", func(c *tctx) error {
			f, _, err := c.FormFile("avatar")
			if err != nil {
				return err
			}
			//nolint:errcheck // The test is done with it.
			defer f.Close()
			return c.String(http.StatusOK, "ok")
		})
		return r
	}
	mounted := func() http.Handler {
		outer := newTestRouter()
		outer.MountRouter("/api", uploads())
		return outer
	}

	for _, tt := range []struct {
		name   string
		target string
		build  func() http.Handler
	}{
		{
			name:   "a middleware that replaced the request",
			target: "/avatars",
			build:  func() http.Handler { return uploads(copyRequest) },
		},
		{
			name:   "a mounted router",
			target: "/api/avatars",
			build:  mounted,
		},
		{
			name:   "no middleware at all",
			target: "/avatars",
			build:  func() http.Handler { return uploads() },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)

			srv := httptest.NewServer(tt.build())
			defer srv.Close()

			body, ct := multipartBody(t, nil, upload{field: "avatar", name: "a.png", content: strings.Repeat("x", 2048)})
			res, err := srv.Client().Post(srv.URL+tt.target, ct, strings.NewReader(body))
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			//nolint:errcheck // The status is what the test reads.
			res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", res.StatusCode)
			}
			srv.Close()

			if n := waitForTempFiles(dir, 0); n != 0 {
				t.Errorf("%d temporary file(s) left behind in %s", n, dir)
			}
		})
	}
}

func waitForTempFiles(dir string, want int) int {
	deadline := time.Now().Add(2 * time.Second)
	for {
		names, _ := filepath.Glob(filepath.Join(dir, "multipart-*"))
		if len(names) == want || time.Now().After(deadline) {
			return len(names)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMultipartFormReadsValuesAndFiles(t *testing.T) {
	r := newTestRouter()
	r.POST("/uploads", func(c *tctx) error {
		form, err := c.MultipartForm()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%s/%d", form.Value["name"][0], len(form.File["photo"])))
	})

	body, ct := multipartBody(t, url.Values{"name": {"bo"}},
		upload{field: "photo", name: "a.png", content: "a"},
		upload{field: "photo", name: "b.png", content: "b"},
	)
	if got, want := post(r, "/uploads", ct, body).Body.String(), "bo/2"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestMultipartFormRejectsABodyThatIsNotMultipart(t *testing.T) {
	r := newTestRouter()
	r.POST("/uploads", func(c *tctx) error {
		_, err := c.MultipartForm()
		return err
	})

	rec := postForm(r, "/uploads", url.Values{"name": {"bo"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestFormValueReadsTheBodyAndNotTheQuery(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		return c.String(http.StatusOK, fmt.Sprintf("%s/%s/%s/%s",
			c.FormValue("name"), c.FormValue("role"),
			c.FormAsDefault("role", "user"), c.FormAsDefault("name", "anon")))
	})

	rec := postForm(r, "/users?role=admin", url.Values{"name": {"bo"}})
	if got, want := rec.Body.String(), "bo//user/bo"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestFormValueSwallowsAParseError(t *testing.T) {
	r := newTestRouter()
	r.MaxBodyBytes(16)
	r.POST("/users", func(c *tctx) error {
		name := c.FormValue("name")
		_, err := c.FormValues()
		return c.String(http.StatusOK, fmt.Sprintf("%q/%v", name, err != nil))
	})

	rec := postForm(r, "/users", url.Values{"name": {strings.Repeat("x", 100)}})
	if got, want := rec.Body.String(), `""/true`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestFormValuesReturnsTheBody(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		vals, err := c.FormValues()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%v/%d", vals["tag"], len(vals)))
	})

	rec := postForm(r, "/users?q=1", url.Values{"tag": {"a", "b"}})
	if got, want := rec.Body.String(), "[a b]/1"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestFormAs(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		age, err := c.FormAs[int]("age")
		if err != nil {
			return err
		}
		ttl, err := c.FormAs[time.Duration]("ttl")
		if err != nil {
			return err
		}
		missing := c.FormAsDefault("missing", 0)
		return c.String(http.StatusOK, fmt.Sprintf("%d/%v/%d", age, ttl, missing))
	})

	rec := postForm(r, "/users", url.Values{"age": {"7"}, "ttl": {"90s"}})
	if got, want := rec.Body.String(), "7/1m30s/0"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}

	if code := postForm(r, "/users", url.Values{"age": {"abc"}}).Code; code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}

func TestBindPath(t *testing.T) {
	type ref struct {
		Org  string `param:"org"`
		Repo string `param:"repo"`
		Page int    `param:"page"`
	}

	r := newTestRouter()
	r.GET("/{org}/{repo}", func(c *tctx) error {
		in, err := c.BindPath[ref]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%s/%s/%d", in.Org, in.Repo, in.Page))
	})

	if got, want := do(r, http.MethodGet, "/go/router").Body.String(), "go/router/0"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestBindPathReportsAParseError(t *testing.T) {
	type ref struct {
		ID int `param:"id"`
	}

	var seen error
	r := newTestRouter()
	r.GET("/users/{id}", func(c *tctx) error {
		_, err := c.BindPath[ref]()
		return err
	})

	r.ErrorHandler(func(c *tctx, err error) error {
		seen = err
		return DefaultErrorHandler(c, err)
	})

	rec := do(r, http.MethodGet, "/users/abc")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := details(t, rec); got != nil {
		t.Errorf("details = %+v, want none", got)
	}
	if got := FieldErrorsOf(seen); len(got) != 1 || got[0].Field != "id" {
		t.Errorf("FieldErrorsOf(err) = %+v, want the field id", got)
	}
}

func TestBindHeader(t *testing.T) {
	type meta struct {
		RequestID string        `header:"x-request-id"`
		Wait      time.Duration `header:"X-Wait"`
		Missing   string        `header:"x-missing"`
	}

	r := newTestRouter()
	r.GET("/ping", func(c *tctx) error {
		in, err := c.BindHeader[meta]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%s/%v/%q", in.RequestID, in.Wait, in.Missing))
	})

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("X-Request-Id", "abc")
	req.Header.Set("X-Wait", "5s")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if got, want := rec.Body.String(), `abc/5s/""`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}

	req = httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("X-Wait", "soon")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := details(t, rec); len(got) != 1 || got[0].Field != "X-Wait" {
		t.Errorf("details = %+v, want the header as the request spells it", got)
	}
}

type signup struct {
	Email string `form:"email" json:"email"`
	Age   int    `form:"age" json:"age"`
}

func (s *signup) Validate() error {
	var errs []error
	if !strings.Contains(s.Email, "@") {
		errs = append(errs, FieldError{Field: "email", Message: "is not an address"})
	}
	if s.Age < 18 {
		errs = append(errs, FieldError{Field: "age", Message: "must be 18 or more"})
	}
	return errors.Join(errs...)
}

func TestValidatorRunsAfterEveryBind(t *testing.T) {
	r := newTestRouter()
	r.POST("/signups", func(c *tctx) error {
		_, err := c.Bind[signup]()
		return err
	})
	r.GET("/signups", func(c *tctx) error {
		_, err := c.BindQuery[signup]()
		return err
	})

	for _, tt := range []struct {
		name string
		rec  *httptest.ResponseRecorder
	}{
		{name: "json", rec: post(r, "/signups", MIMEApplicationJSON, `{"email":"bo","age":9}`)},
		{name: "form", rec: postForm(r, "/signups", url.Values{"email": {"bo"}, "age": {"9"}})},
		{name: "query", rec: do(r, http.MethodGet, "/signups?email=bo&age=9")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", tt.rec.Code, tt.rec.Body)
			}
			got := details(t, tt.rec)
			if len(got) != 2 || got[0].Field != "email" || got[1].Field != "age" {
				t.Errorf("details = %+v", got)
			}
		})
	}
}

func TestValidatorPassesAGoodValue(t *testing.T) {
	r := newTestRouter()
	r.POST("/signups", func(c *tctx) error {
		in, err := c.Bind[signup]()
		if err != nil {
			return err
		}
		return c.String(http.StatusCreated, in.Email)
	})

	rec := post(r, "/signups", MIMEApplicationJSON, `{"email":"bo@example.com","age":30}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
}

type plainInvalid struct {
	Name string `json:"name"`
}

func (p *plainInvalid) Validate() error { return errors.New("the name is taken") }

func TestValidatorWithoutFieldErrorsReportsNoDetails(t *testing.T) {
	r := newTestRouter()
	r.POST("/names", func(c *tctx) error {
		_, err := c.Bind[plainInvalid]()
		return err
	})

	rec := post(r, "/names", MIMEApplicationJSON, `{"name":"bo"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if got := details(t, rec); len(got) != 0 {
		t.Errorf("details = %+v, want none", got)
	}
	if strings.Contains(rec.Body.String(), "the name is taken") {
		t.Errorf("body leaks the internal cause: %q", rec.Body.String())
	}
}

// validateCases maps the name a request binds to what Validate returns.
var validateCases = map[string]error{
	"message":        ErrConflict.WithMessage("the name is taken"),
	"wrapped":        fmt.Errorf("check: %w", ErrConflict.WithMessage("the name is taken")),
	"details":        ErrUnprocessableEntity.WithMessage("check the form").WithDetails([]FieldError{{Field: "email", Message: "is not an address"}}),
	"coder":          &codedError{status: http.StatusConflict},
	"coder of zero":  &codedError{},
	"zero status":    &HTTPError{Message: "x"},
	"server error":   ErrInternalServerError.WithMessage("x"),
	"field and http": errors.Join(FieldError{Field: "email", Message: "is not an address"}, ErrConflict),
	"typed nil":      (*HTTPError)(nil),
}

type chosenInvalid struct {
	Case string `json:"case"`
}

func (c *chosenInvalid) Validate() error { return validateCases[c.Case] }

func TestValidatorKeepsTheStatusItNames(t *testing.T) {
	var bindErr error
	r := newTestRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.POST("/names", func(c *tctx) error {
		_, bindErr = c.Bind[chosenInvalid]()
		return bindErr
	})

	for _, tt := range []struct {
		name       string
		wantStatus int
		wantBody   string
	}{
		{name: "message", wantStatus: http.StatusConflict, wantBody: "the name is taken"},
		{name: "wrapped", wantStatus: http.StatusConflict, wantBody: "the name is taken"},
		{name: "details", wantStatus: http.StatusUnprocessableEntity, wantBody: "check the form\nemail: is not an address"},
		{name: "coder", wantStatus: http.StatusConflict, wantBody: "Conflict"},
		{name: "coder of zero", wantStatus: http.StatusUnprocessableEntity, wantBody: "Unprocessable Entity"},
		{name: "zero status", wantStatus: http.StatusUnprocessableEntity, wantBody: "Unprocessable Entity"},
		{name: "server error", wantStatus: http.StatusInternalServerError, wantBody: "x"},
		{name: "field and http", wantStatus: http.StatusConflict, wantBody: "Conflict"},
		{name: "typed nil", wantStatus: http.StatusUnprocessableEntity, wantBody: "Unprocessable Entity"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bindErr = nil
			rec := post(r, "/names", MIMEApplicationJSON, `{"case":"`+tt.name+`"}`)
			if rec.Code != tt.wantStatus || rec.Body.String() != tt.wantBody {
				t.Errorf("answer = %d %q, want %d %q", rec.Code, rec.Body.String(), tt.wantStatus, tt.wantBody)
			}
			if _, ok := errors.AsType[*HTTPError](bindErr); !ok {
				t.Errorf("Bind error = %#v, want an *HTTPError in it", bindErr)
			}
			if tt.wantStatus == http.StatusConflict && !errors.Is(bindErr, ErrConflict) {
				t.Errorf("errors.Is(%v, ErrConflict) = false", bindErr)
			}
		})
	}
}

func TestBindFormReturnsWhatDecoded(t *testing.T) {
	var in signup
	var bindErr error
	r := newTestRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.POST("/signups", func(c *tctx) error {
		in, bindErr = c.BindForm[signup]()
		return bindErr
	})
	r.POST("/small", func(c *tctx) error {
		c.SetBodyLimit(16)
		in, bindErr = c.BindForm[signup]()
		return bindErr
	})

	t.Run("a field that does not decode", func(t *testing.T) {
		postForm(r, "/signups", url.Values{"email": {"bo@x.io"}, "age": {"abc"}})
		if in.Email != "bo@x.io" {
			t.Errorf("Email = %q, want the value that decoded", in.Email)
		}
		want := []FieldError{{Field: "age", Message: `cannot parse "abc" as int`}}
		if got := FieldErrorsOf(bindErr); !reflect.DeepEqual(got, want) {
			t.Errorf("FieldErrorsOf = %+v, want %+v", got, want)
		}
		if got := StatusOf(bindErr); got != http.StatusBadRequest {
			t.Errorf("StatusOf = %d, want 400", got)
		}
	})

	t.Run("a value that Validate refuses", func(t *testing.T) {
		postForm(r, "/signups", url.Values{"email": {"bo"}, "age": {"9"}})
		if in.Email != "bo" || in.Age != 9 {
			t.Errorf("in = %+v, want the bound value", in)
		}
		got := FieldErrorsOf(bindErr)
		if len(got) != 2 || got[0].Field != "email" || got[1].Field != "age" {
			t.Errorf("FieldErrorsOf = %+v, want email and age", got)
		}
		if got := StatusOf(bindErr); got != http.StatusUnprocessableEntity {
			t.Errorf("StatusOf = %d, want 422", got)
		}
	})

	t.Run("a body over the limit", func(t *testing.T) {
		post(r, "/small", MIMEApplicationForm, "email="+strings.Repeat("x", 100))
		if in != (signup{}) {
			t.Errorf("in = %+v, want the zero value", in)
		}
		if got := FieldErrorsOf(bindErr); got != nil {
			t.Errorf("FieldErrorsOf = %+v, want nil", got)
		}
		if got := StatusOf(bindErr); got != http.StatusRequestEntityTooLarge {
			t.Errorf("StatusOf = %d, want 413", got)
		}
	})
}

type countedValidate struct {
	Age int `form:"age" json:"age"`
}

var countedValidateRuns int

func (*countedValidate) Validate() error {
	countedValidateRuns++
	return nil
}

func TestValidatorRunsOnlyOnAFullyDecodedValue(t *testing.T) {
	r := newTestRouter()
	r.POST("/ages", func(c *tctx) error {
		_, err := c.Bind[countedValidate]()
		return err
	})

	for _, tt := range []struct {
		name string
		rec  func() *httptest.ResponseRecorder
	}{
		{name: "form", rec: func() *httptest.ResponseRecorder { return postForm(r, "/ages", url.Values{"age": {"abc"}}) }},
		{name: "json", rec: func() *httptest.ResponseRecorder { return post(r, "/ages", MIMEApplicationJSON, `{"age":"x"}`) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			countedValidateRuns = 0
			rec := tt.rec()
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
			if got := details(t, rec); len(got) != 1 || got[0].Field != "age" {
				t.Errorf("details = %+v, want age", got)
			}
			if countedValidateRuns != 0 {
				t.Errorf("Validate ran %d times, want 0", countedValidateRuns)
			}
		})
	}
}

func TestBindReportsEveryFieldThatDidNotFit(t *testing.T) {
	type filter struct {
		Page  int           `query:"page"`
		Limit int           `query:"limit"`
		TTL   time.Duration `query:"ttl"`
	}

	r := newTestRouter()
	r.GET("/search", func(c *tctx) error {
		_, err := c.Bind[filter]()
		return err
	})

	rec := do(r, http.MethodGet, "/search?page=a&limit=b&ttl=c")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	got := details(t, rec)
	if len(got) != 3 {
		t.Fatalf("details = %+v, want three", got)
	}
	for i, want := range []string{"page", "limit", "ttl"} {
		if got[i].Field != want {
			t.Errorf("details[%d].Field = %q, want %q", i, got[i].Field, want)
		}
		if got[i].Message == "" {
			t.Errorf("details[%d] carries no message", i)
		}
	}
}

func TestQueryAs(t *testing.T) {
	r := newTestRouter()
	r.GET("/search", func(c *tctx) error {
		page, err := c.QueryAs[int]("page")
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%d", page))
	})

	for _, tt := range []struct {
		target string
		want   string
		code   int
	}{
		{target: "/search?page=2", want: "2", code: http.StatusOK},
		{target: "/search?page=", want: `missing query parameter "page"`, code: http.StatusBadRequest},
		{target: "/search", want: `missing query parameter "page"`, code: http.StatusBadRequest},
		{target: "/search?page=abc", want: "query parameter page: ", code: http.StatusBadRequest},
	} {
		t.Run(tt.target, func(t *testing.T) {
			rec := do(r, http.MethodGet, tt.target)
			if rec.Code != tt.code {
				t.Fatalf("status = %d, want %d", rec.Code, tt.code)
			}
			if !strings.HasPrefix(rec.Body.String(), tt.want) {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.want)
			}
		})
	}
}

func TestQueryAllAs(t *testing.T) {
	r := newTestRouter()
	r.GET("/search", func(c *tctx) error {
		ids, err := c.QueryAllAs[int]("id")
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%v/%d", ids, len(ids)))
	})

	if got, want := do(r, http.MethodGet, "/search?id=1&id=2").Body.String(), "[1 2]/2"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if got, want := do(r, http.MethodGet, "/search").Body.String(), "[]/0"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if code := do(r, http.MethodGet, "/search?id=1&id=x").Code; code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}

func TestParamAsDefault(t *testing.T) {
	r := newTestRouter()
	r.GET("/users/{id}", func(c *tctx) error {
		return c.String(http.StatusOK, fmt.Sprintf("%d/%d",
			c.ParamAsDefault("id", 7), c.ParamAsDefault("missing", 3)))
	})

	if got, want := do(r, http.MethodGet, "/users/9").Body.String(), "9/3"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if got, want := do(r, http.MethodGet, "/users/abc").Body.String(), "7/3"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestParseValue(t *testing.T) {
	page, err := ParseValue[int]("42")
	if err != nil || page != 42 {
		t.Errorf("ParseValue = %d, %v", page, err)
	}
	if _, err := ParseValue[int]("abc"); err == nil {
		t.Error("ParseValue accepted a value that does not parse")
	}
	if _, ok := errors.AsType[*HTTPError](func() error { _, err := ParseValue[int]("abc"); return err }()); ok {
		t.Error("ParseValue returned an HTTPError, which is the caller's choice to make")
	}
	unset, err := ParseValue[*int]("")
	if err == nil || unset != nil {
		t.Errorf("ParseValue[*int] of an empty string = %v, %v, want nil and an error", unset, err)
	}
	when, err := ParseValue[time.Time]("2026-01-02T03:04:05Z")
	if err != nil || when.Year() != 2026 {
		t.Errorf("ParseValue = %v, %v", when, err)
	}
}

func TestAsDefaultFallsBack(t *testing.T) {
	r := newTestRouter()
	r.POST("/{ttl}", func(c *tctx) error {
		return c.String(http.StatusOK, fmt.Sprintf("%v/%v/%v/%v",
			c.ParamAsDefault("ttl", time.Minute),
			c.ParamAsDefault("missing", time.Minute),
			c.QueryAsDefault("ttl", time.Minute),
			c.FormAsDefault("ttl", time.Minute)))
	})

	for _, tt := range []struct {
		name, target, body, want string
	}{
		{name: "a value", target: "/90s?ttl=90s", body: "ttl=90s", want: "1m30s/1m0s/1m30s/1m30s"},
		{name: "a malformed value", target: "/nope?ttl=nope", body: "ttl=nope", want: "1m0s/1m0s/1m0s/1m0s"},
		{name: "an empty value", target: "/nope?ttl=", body: "ttl=", want: "1m0s/1m0s/1m0s/1m0s"},
		{name: "no value", target: "/nope", want: "1m0s/1m0s/1m0s/1m0s"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := post(r, tt.target, MIMEApplicationForm, tt.body).Body.String(); got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
		})
	}
}

type pointerValidated struct {
	Name string `json:"name"`
}

func (p *pointerValidated) Validate() error { return errors.New("validate ran") }

// validate is handed &v, which is **T for a pointer T, and a pointer to a
// pointer has an empty method set.
func TestValidateRunsForAPointerTypeArgument(t *testing.T) {
	r := newTestRouter()
	r.POST("/ptr", func(c *tctx) error {
		if _, err := c.Bind[*pointerValidated](); err != nil {
			return err
		}
		return c.String(http.StatusOK, "ok")
	})
	r.POST("/val", func(c *tctx) error {
		if _, err := c.Bind[pointerValidated](); err != nil {
			return err
		}
		return c.String(http.StatusOK, "ok")
	})

	for _, path := range []string{"/ptr", "/val"} {
		rec := doBody(r, http.MethodPost, path, MIMEApplicationJSON, `{"name":"x"}`)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("POST %s = %d, want 422", path, rec.Code)
		}
	}
}

// ParseForm on a multipart body fills PostForm and leaves MultipartForm nil, so
// PostForm alone cannot say whether the body has been read.
func TestFormFileSurvivesAnEarlierParseForm(t *testing.T) {
	r := newTestRouter()
	r.Use(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			_ = c.Request().ParseForm()
			return next(c)
		}
	})
	r.POST("/upload", func(c *tctx) error {
		_, fh, err := c.FormFile("doc")
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fh.Filename)
	})

	body := "--B\r\nContent-Disposition: form-data; name=\"doc\"; filename=\"a.txt\"\r\n\r\nhello\r\n--B--\r\n"
	rec := doBody(r, http.MethodPost, "/upload", "multipart/form-data; boundary=B", body)
	if rec.Code != http.StatusOK || rec.Body.String() != "a.txt" {
		t.Errorf("upload after ParseForm = %d %q, want 200 %q", rec.Code, rec.Body.String(), "a.txt")
	}
}

// A body method with no Content-Type must not fall through to the query.
func TestBindRefusesABodyWithNoContentType(t *testing.T) {
	r := newTestRouter()
	r.POST("/x", func(c *tctx) error {
		v, err := c.Bind[struct {
			Name string `json:"name"`
		}]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, v.Name)
	})
	rec := doBody(r, http.MethodPost, "/x?name=fromquery", "", `{"name":"frombody"}`)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("POST with no Content-Type = %d %q, want 415", rec.Code, rec.Body.String())
	}
}

// HTTPError has exported fields, so one can be built without a status, and
// WriteHeader panics on 0.
func TestHTTPErrorWithoutAStatusIsAnInternalError(t *testing.T) {
	r := newTestRouter()
	r.GET("/z", func(*tctx) error { return &HTTPError{Message: "custom"} })
	rec := do(r, http.MethodGet, "/z")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "custom") {
		t.Errorf("body = %q, want it to carry the message", rec.Body.String())
	}
}

// MaxBytesReader marks the connection for closing through an unexported method
// on the writer it is handed, and does not unwrap.
func TestOversizedBodyClosesTheConnection(t *testing.T) {
	r := newTestRouter()
	r.MaxBodyBytes(16)
	r.POST("/b", func(c *tctx) error {
		_, err := c.BindJSON[map[string]any]()
		return err
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	body := `{"k":"` + strings.Repeat("a", 4096) + `"}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/b", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(HeaderContentType, MIMEApplicationJSON)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // The test is done with it.

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
	if !resp.Close {
		t.Error("the server kept the connection open after a 413")
	}
}

// A body of "null" is the client's choice, and leaves nothing to hand the
// handler, so it is refused rather than passed on as a nil pointer.
func TestBindPointerWithANullBodyIsRefused(t *testing.T) {
	r := newTestRouter()
	r.POST("/p", func(c *tctx) error {
		if _, err := c.Bind[*pointerValidated](); err != nil {
			return err
		}
		return c.String(http.StatusOK, "handler ran")
	})
	rec := doBody(r, http.MethodPost, "/p", MIMEApplicationJSON, `null`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("null body = %d %q, want 400", rec.Code, rec.Body.String())
	}
}

// A checkbox with no value attribute sends "on" when it is checked and
// nothing when it is not.
func TestBindFormReadsACheckbox(t *testing.T) {
	type prefs struct {
		News   *bool `form:"news"`
		Accept bool  `form:"accept"`
	}

	r := newTestRouter()
	r.POST("/prefs", func(c *tctx) error {
		in, err := c.BindForm[prefs]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%v/%v", in.Accept, in.News))
	})

	tests := []struct {
		form url.Values
		want string
	}{
		{form: url.Values{"accept": {"on"}}, want: "true/<nil>"},
		{form: url.Values{"accept": {"off"}}, want: "false/<nil>"},
		{form: url.Values{}, want: "false/<nil>"},
	}
	for _, tt := range tests {
		rec := postForm(r, "/prefs", tt.form)
		if rec.Code != http.StatusOK || rec.Body.String() != tt.want {
			t.Errorf("BindForm(%s) = %d %q, want 200 %q", tt.form.Encode(), rec.Code, rec.Body.String(), tt.want)
		}
	}
}

func TestBindQueryReadsACheckbox(t *testing.T) {
	type filter struct {
		Open bool `query:"open"`
	}

	r := newTestRouter()
	r.GET("/issues", func(c *tctx) error {
		in, err := c.BindQuery[filter]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%v", in.Open))
	})

	for target, want := range map[string]string{
		"/issues?open=on":  "true",
		"/issues?open=OFF": "false",
		"/issues":          "false",
	} {
		rec := do(r, http.MethodGet, target)
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("GET %s = %d %q, want 200 %q", target, rec.Code, rec.Body.String(), want)
		}
	}
}

func TestFormAsReadsACheckbox(t *testing.T) {
	r := newTestRouter()
	r.POST("/prefs", func(c *tctx) error {
		on, err := c.FormAs[bool]("accept")
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%v", on))
	})

	rec := postForm(r, "/prefs", url.Values{"accept": {"on"}})
	if rec.Code != http.StatusOK || rec.Body.String() != "true" {
		t.Errorf("FormAs[bool] of on = %d %q, want 200 %q", rec.Code, rec.Body.String(), "true")
	}
}

func TestParseValueReadsOnAndOff(t *testing.T) {
	for in, want := range map[string]bool{"on": true, "On": true, "off": false, "OFF": false} {
		got, err := ParseValue[bool](in)
		if err != nil || got != want {
			t.Errorf("ParseValue[bool](%q) = %v, %v, want %v", in, got, err, want)
		}
	}
	if _, err := ParseValue[bool]("yes"); err == nil {
		t.Error("ParseValue[bool] accepted yes")
	}
}

// net/http fails a form parse on a malformed query string, which would turn a
// good body into a 400.
func TestFormReadersIgnoreAMalformedQuery(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		in, err := c.BindForm[struct {
			Name string `form:"name"`
		}]()
		if err != nil {
			return err
		}
		if _, err := c.FormValues(); err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%s/%s", in.Name, c.Request().FormValue("name")))
	})
	r.POST("/upload", func(c *tctx) error {
		name := c.FormValue("name")
		form, err := c.MultipartForm()
		if err != nil {
			return err
		}
		_, fh, err := c.FormFile("doc")
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%s/%s/%d", name, fh.Filename, len(form.File)))
	})

	rec := post(r, "/users?q=%zz&a;b", MIMEApplicationForm, "name=bo")
	if rec.Code != http.StatusOK || rec.Body.String() != "bo/bo" {
		t.Errorf("URL-encoded = %d %q, want 200 %q", rec.Code, rec.Body.String(), "bo/bo")
	}

	body, ct := multipartBody(t, url.Values{"name": {"bo"}}, upload{field: "doc", name: "a.txt", content: "hi"})
	rec = post(r, "/upload?q=%zz", ct, body)
	if rec.Code != http.StatusOK || rec.Body.String() != "bo/a.txt/1" {
		t.Errorf("multipart = %d %q, want 200 %q", rec.Code, rec.Body.String(), "bo/a.txt/1")
	}
}

func TestFormReadersKeepAFormAnEarlierReaderBuilt(t *testing.T) {
	r := newTestRouter()
	r.Use(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			_ = c.Request().ParseForm()
			return next(c)
		}
	})
	r.POST("/upload", func(c *tctx) error {
		if _, _, err := c.FormFile("doc"); err != nil {
			return err
		}
		return c.String(http.StatusOK, c.Request().Form.Get("q"))
	})

	body, ct := multipartBody(t, nil, upload{field: "doc", name: "a.txt", content: "hi"})
	rec := post(r, "/upload?q=1", ct, body)
	if rec.Code != http.StatusOK || rec.Body.String() != "1" {
		t.Errorf("Form after FormFile = %d %q, want 200 %q", rec.Code, rec.Body.String(), "1")
	}
}

type unwrappingWriter struct{ http.ResponseWriter }

func (w unwrappingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Gzip and HTMXRedirect put a wrapper into the ResponseWriter of the Response,
// and MaxBytesReader does not unwrap.
func TestOversizedBodyClosesTheConnectionBehindAWrappedWriter(t *testing.T) {
	r := newTestRouter()
	r.MaxBodyBytes(16)
	r.Use(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			res := c.Response()
			res.ResponseWriter = unwrappingWriter{res.ResponseWriter}
			return next(c)
		}
	})
	r.POST("/b", func(c *tctx) error {
		_, err := c.BindJSON[map[string]any]()
		return err
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	body := `{"k":"` + strings.Repeat("a", 4096) + `"}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/b", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(HeaderContentType, MIMEApplicationJSON)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // The test is done with it.

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
	if !resp.Close {
		t.Error("the server kept the connection open after a 413")
	}
}

func setBodyLimitRouter(limit int64) *Router[*tctx] {
	r := newTestRouter()
	r.MaxBodyBytes(16)
	r.POST("/bind", func(c *tctx) error {
		c.SetBodyLimit(limit)
		in, err := c.BindJSON[map[string]string]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, in["k"])
	})
	r.POST("/read", func(c *tctx) error {
		c.SetBodyLimit(limit)
		n, err := io.Copy(io.Discard, c.Request().Body)
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return c.String(http.StatusRequestEntityTooLarge, "MaxBytesError")
		}
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%d", n))
	})
	return r
}

func jsonOfLength(n int) string {
	return `{"k":"` + strings.Repeat("a", n-len(`{"k":""}`)) + `"}`
}

func TestSetBodyLimitRaisesTheCapOfTheRouter(t *testing.T) {
	r := setBodyLimitRouter(1 << 10)
	body := jsonOfLength(100)

	rec := doBody(r, http.MethodPost, "/bind", MIMEApplicationJSON, body)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d %q, want 200", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader(body))
	req.Header.Set(HeaderContentType, MIMEApplicationJSON)
	req.ContentLength = -1
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("chunked: status = %d %q, want 200", rec.Code, rec.Body.String())
	}
}

func TestSetBodyLimitLowersTheCapOfTheRouter(t *testing.T) {
	r := newTestRouter()
	r.POST("/bind", func(c *tctx) error {
		c.SetBodyLimit(16)
		_, err := c.BindJSON[map[string]string]()
		return err
	})
	r.POST("/read", func(c *tctx) error {
		c.SetBodyLimit(16)
		_, err := io.Copy(io.Discard, c.Request().Body)
		if _, ok := errors.AsType[*http.MaxBytesError](err); !ok {
			return ErrInternalServerError.WithMessage("read error = %v, want a MaxBytesError", err)
		}
		return c.NoContent(http.StatusNoContent)
	})

	body := jsonOfLength(100)
	if rec := doBody(r, http.MethodPost, "/bind", MIMEApplicationJSON, body); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("Bind: status = %d, want 413", rec.Code)
	}
	if rec := doBody(r, http.MethodPost, "/read", MIMEApplicationJSON, body); rec.Code != http.StatusNoContent {
		t.Errorf("raw read: %d %q", rec.Code, rec.Body.String())
	}
}

func TestSetBodyLimitZeroLiftsTheCapOfTheRouter(t *testing.T) {
	r := setBodyLimitRouter(0)
	body := jsonOfLength(4 << 10)

	if rec := doBody(r, http.MethodPost, "/bind", MIMEApplicationJSON, body); rec.Code != http.StatusOK {
		t.Errorf("Bind: status = %d, want 200", rec.Code)
	}
	if rec := doBody(r, http.MethodPost, "/read", MIMEApplicationJSON, body); rec.Code != http.StatusOK {
		t.Errorf("raw read: status = %d %q, want 200", rec.Code, rec.Body.String())
	}
}

func TestSetBodyLimitKeepsASmallerCapOnTheBody(t *testing.T) {
	r := newTestRouter()
	r.POST("/bind", func(c *tctx) error {
		c.SetBodyLimit(16)
		c.SetBodyLimit(0)
		_, err := c.BindJSON[map[string]string]()
		return err
	})

	if rec := doBody(r, http.MethodPost, "/bind", MIMEApplicationJSON, jsonOfLength(100)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestSetBodyLimitReachesTheFormReaders(t *testing.T) {
	r := newTestRouter()
	r.MaxBodyBytes(64)
	r.Use(func(next HandlerFunc[*tctx]) HandlerFunc[*tctx] {
		return func(c *tctx) error {
			c.SetBodyLimit(4 << 10)
			return next(c)
		}
	})
	r.POST("/form", func(c *tctx) error {
		return c.String(http.StatusOK, fmt.Sprintf("%d", len(c.FormValue("name"))))
	})
	r.POST("/upload", func(c *tctx) error {
		form, err := c.MultipartForm()
		if err != nil {
			return err
		}
		_, fh, err := c.FormFile("doc")
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%d/%d", len(form.File), fh.Size))
	})

	rec := postForm(r, "/form", url.Values{"name": {strings.Repeat("x", 1000)}})
	if rec.Code != http.StatusOK || rec.Body.String() != "1000" {
		t.Errorf("FormValue = %d %q, want 200 %q", rec.Code, rec.Body.String(), "1000")
	}

	body, ct := multipartBody(t, nil, upload{field: "doc", name: "a.txt", content: strings.Repeat("x", 1000)})
	rec = post(r, "/upload", ct, body)
	if rec.Code != http.StatusOK || rec.Body.String() != "1/1000" {
		t.Errorf("FormFile = %d %q, want 200 %q", rec.Code, rec.Body.String(), "1/1000")
	}
}

func TestSetBodyLimitWithoutABodyAllocatesNothing(t *testing.T) {
	b := NewBase(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if allocs := testing.AllocsPerRun(100, func() { b.SetBodyLimit(1 << 10) }); allocs != 0 {
		t.Errorf("SetBodyLimit on a request without a body allocated %v times, want 0", allocs)
	}
	if b.deferred != nil {
		t.Error("SetBodyLimit on a request without a body kept a cap")
	}
}

func TestFormAsReportsAMissingField(t *testing.T) {
	var got error
	r := newTestRouter()
	r.MaxBodyBytes(64)
	r.POST("/confirm", func(c *tctx) error {
		token, err := c.FormAs[string]("token")
		got = err
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, token)
	})

	missing := []FieldError{{Field: "token", Message: "is required"}}
	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
		wantBody    string
		wantFields  []FieldError
	}{
		{name: "present", contentType: MIMEApplicationForm, body: "token=abc", wantStatus: http.StatusOK, wantBody: "abc"},
		{name: "absent", contentType: MIMEApplicationForm, body: "other=1", wantStatus: http.StatusBadRequest, wantFields: missing},
		{name: "empty", contentType: MIMEApplicationForm, body: "token=", wantStatus: http.StatusBadRequest, wantFields: missing},
		{name: "a JSON body", contentType: MIMEApplicationJSON, body: `{"token":"abc"}`, wantStatus: http.StatusBadRequest, wantFields: missing},
		{
			name: "over the limit", contentType: MIMEApplicationForm,
			body: "token=" + strings.Repeat("x", 100), wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name: "a malformed multipart body", contentType: "multipart/form-data; boundary=B",
			body: "not multipart", wantStatus: http.StatusBadRequest, wantBody: "malformed form body",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got = nil
			rec := post(r, "/confirm", tt.contentType, tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d %q, want %d", rec.Code, rec.Body.String(), tt.wantStatus)
			}
			if tt.wantBody != "" && strings.TrimSpace(rec.Body.String()) != tt.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.wantBody)
			}
			if tt.wantFields == nil {
				return
			}
			he, ok := errors.AsType[*HTTPError](got)
			if !ok {
				t.Fatalf("error = %v, want an *HTTPError", got)
			}
			if want := `missing form field "token"`; he.Message != want || !reflect.DeepEqual(he.Details, tt.wantFields) {
				t.Errorf("error = %q %#v, want %q %#v", he.Message, he.Details, want, tt.wantFields)
			}
			if fe, ok := errors.AsType[FieldError](got); !ok || fe != tt.wantFields[0] {
				t.Errorf("errors.AsType[FieldError] = %#v, %v, want %#v", fe, ok, tt.wantFields[0])
			}
			if fields := FieldErrorsOf(got); !reflect.DeepEqual(fields, tt.wantFields) {
				t.Errorf("FieldErrorsOf = %#v, want exactly %#v", fields, tt.wantFields)
			}
		})
	}
}

func TestBindFormLeavesAFieldOfAnotherSourceAlone(t *testing.T) {
	type input struct {
		ID      string `param:"id"`
		IsAdmin bool
	}
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		in, err := c.BindForm[input]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%q/%v", in.ID, in.IsAdmin))
	})

	rec := postForm(r, "/users", url.Values{"id": {"evil"}, "isadmin": {"on"}})
	if got, want := rec.Body.String(), `""/false`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// Each struct tags Own for the source under test, Other for another source,
// JSON for JSON alone, and leaves Untagged bare.
type (
	formSourced struct {
		Own      string `form:"own"`
		Other    string `query:"other"`
		JSON     string `json:"json"`
		Untagged string
	}
	querySourced struct {
		Own      string `query:"own"`
		Other    string `form:"other"`
		JSON     string `json:"json"`
		Untagged string
	}
	paramSourced struct {
		Own      string `param:"own"`
		Other    string `header:"other"`
		JSON     string `json:"json"`
		Untagged string
	}
	headerSourced struct {
		Own      string `header:"own"`
		Other    string `param:"other"`
		JSON     string `json:"json"`
		Untagged string
	}
)

func sourcedString(own, other, json, untagged string) string {
	return fmt.Sprintf("%q/%q/%q/%q", own, other, json, untagged)
}

func TestBindFillsOnlyTheFieldsTaggedForItsSource(t *testing.T) {
	r := newTestRouter()
	r.POST("/form", func(c *tctx) error {
		in, err := c.BindForm[formSourced]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, sourcedString(in.Own, in.Other, in.JSON, in.Untagged))
	})
	r.GET("/query", func(c *tctx) error {
		in, err := c.BindQuery[querySourced]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, sourcedString(in.Own, in.Other, in.JSON, in.Untagged))
	})
	r.GET("/path/{own}/{other}/{json}/{untagged}/{Untagged}/{JSON}", func(c *tctx) error {
		in, err := c.BindPath[paramSourced]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, sourcedString(in.Own, in.Other, in.JSON, in.Untagged))
	})
	r.GET("/header", func(c *tctx) error {
		in, err := c.BindHeader[headerSourced]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, sourcedString(in.Own, in.Other, in.JSON, in.Untagged))
	})

	every := url.Values{
		"own": {"v"}, "other": {"v"}, "json": {"v"}, "JSON": {"v"},
		"untagged": {"v"}, "Untagged": {"v"},
	}
	want := sourcedString("v", "", "", "")

	t.Run("form", func(t *testing.T) {
		if got := postForm(r, "/form", every).Body.String(); got != want {
			t.Errorf("body = %s, want %s", got, want)
		}
	})
	t.Run("query", func(t *testing.T) {
		if got := do(r, http.MethodGet, "/query?"+every.Encode()).Body.String(); got != want {
			t.Errorf("body = %s, want %s", got, want)
		}
	})
	t.Run("path", func(t *testing.T) {
		if got := do(r, http.MethodGet, "/path/v/v/v/v/v/v").Body.String(); got != want {
			t.Errorf("body = %s, want %s", got, want)
		}
	})
	t.Run("header", func(t *testing.T) {
		headers := map[string]string{"Own": "v", "Other": "v", "Json": "v", "Untagged": "v"}
		if got := doWithHeaders(r, http.MethodGet, "/header", headers).Body.String(); got != want {
			t.Errorf("body = %s, want %s", got, want)
		}
	})
}

func TestBindReadsTheBodyOfEveryMethod(t *testing.T) {
	type input struct {
		Name string `form:"name" json:"name"`
	}
	r := newTestRouter()
	bind := func(c *tctx) error {
		in, err := c.Bind[input]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, in.Name+"/"+c.FormValue("name"))
	}
	r.Handle(MethodQuery, "/x", bind)
	r.DELETE("/x", bind)

	for _, method := range []string{MethodQuery, http.MethodDelete} {
		t.Run(method+" form", func(t *testing.T) {
			rec := doBody(r, method, "/x", MIMEApplicationForm, "name=ann")
			if got, want := rec.Body.String(), "ann/ann"; got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
		})
		t.Run(method+" json", func(t *testing.T) {
			rec := doBody(r, method, "/x", MIMEApplicationJSON, `{"name":"ann"}`)
			if got, want := rec.Body.String(), "ann/"; got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
		})
	}
}

func TestFormReadersApplyTheBodyLimitToEveryMethod(t *testing.T) {
	r := newTestRouter()
	r.MaxBodyBytes(8)
	r.DELETE("/x", func(c *tctx) error {
		_, err := c.FormValues()
		return err
	})

	rec := doBody(r, http.MethodDelete, "/x", MIMEApplicationForm, "name="+strings.Repeat("a", 64))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestBindTakesAPointerToAStruct(t *testing.T) {
	type input struct {
		Name string `form:"name" query:"name" param:"name" header:"X-Name" json:"name"`
	}
	r := newTestRouter()
	reply := func(c *tctx, in *input, err error) error {
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, in.Name)
	}
	r.GET("/query", func(c *tctx) error { in, err := c.BindQuery[*input](); return reply(c, in, err) })
	r.POST("/form", func(c *tctx) error { in, err := c.BindForm[*input](); return reply(c, in, err) })
	r.GET("/path/{name}", func(c *tctx) error { in, err := c.BindPath[*input](); return reply(c, in, err) })
	r.GET("/header", func(c *tctx) error { in, err := c.BindHeader[*input](); return reply(c, in, err) })
	r.GET("/bind", func(c *tctx) error { in, err := c.Bind[*input](); return reply(c, in, err) })
	r.POST("/json", func(c *tctx) error { in, err := c.BindJSON[*input](); return reply(c, in, err) })

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"query":  do(r, http.MethodGet, "/query?name=ann"),
		"form":   postForm(r, "/form", url.Values{"name": {"ann"}}),
		"path":   do(r, http.MethodGet, "/path/ann"),
		"header": doWithHeaders(r, http.MethodGet, "/header", map[string]string{"X-Name": "ann"}),
		"bind":   do(r, http.MethodGet, "/bind?name=ann"),
		"json":   post(r, "/json", MIMEApplicationJSON, `{"name":"ann"}`),
	} {
		if rec.Code != http.StatusOK || rec.Body.String() != "ann" {
			t.Errorf("%s: %d %q, want 200 ann", name, rec.Code, rec.Body)
		}
	}
}

func TestBindAnswers500ForATargetThatIsNotAStruct(t *testing.T) {
	var seen error
	r := newTestRouter()
	r.ErrorHandler(func(c *tctx, err error) error {
		seen = err
		return DefaultErrorHandler(c, err)
	})
	r.GET("/query", func(c *tctx) error { _, err := c.BindQuery[[]string](); return err })
	r.POST("/form", func(c *tctx) error { _, err := c.BindForm[*int](); return err })
	r.GET("/path/{a}", func(c *tctx) error { _, err := c.BindPath[map[string]string](); return err })
	r.GET("/header", func(c *tctx) error { _, err := c.BindHeader[string](); return err })
	r.GET("/bind", func(c *tctx) error { _, err := c.Bind[**struct{}](); return err })

	for name, rec := range map[string]func() *httptest.ResponseRecorder{
		"query":  func() *httptest.ResponseRecorder { return do(r, http.MethodGet, "/query?a=1") },
		"form":   func() *httptest.ResponseRecorder { return postForm(r, "/form", url.Values{"a": {"1"}}) },
		"path":   func() *httptest.ResponseRecorder { return do(r, http.MethodGet, "/path/1") },
		"header": func() *httptest.ResponseRecorder { return do(r, http.MethodGet, "/header") },
		"bind":   func() *httptest.ResponseRecorder { return do(r, http.MethodGet, "/bind?a=1") },
	} {
		seen = nil
		got := rec()
		if got.Code != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500", name, got.Code)
		}
		if strings.Contains(got.Body.String(), "struct") {
			t.Errorf("%s: body = %q, want no word of the decoder in it", name, got.Body)
		}
		if he, ok := errors.AsType[*HTTPError](seen); !ok || he.Err == nil {
			t.Errorf("%s: error = %v, want an HTTPError with the cause", name, seen)
		}
	}
}

func TestParseValueRejectsAnEmptyValue(t *testing.T) {
	if _, err := ParseValue[int](""); err == nil {
		t.Error(`ParseValue[int]("") reported no error`)
	}
	if _, err := ParseValue[*bool](""); err == nil {
		t.Error(`ParseValue[*bool]("") reported no error`)
	}
	if v, err := ParseValue[string](""); err != nil || v != "" {
		t.Errorf(`ParseValue[string]("") = %q, %v, want "", nil`, v, err)
	}
}

func TestBindCombinesTheSourcesByTag(t *testing.T) {
	type input struct {
		Org     string `param:"org"`
		Page    int    `query:"page"`
		Trace   string `header:"X-Trace"`
		Name    string `json:"name" form:"name"`
		ID      string `json:"id" form:"id" param:"org"`
		IsAdmin bool
	}
	r := newTestRouter()
	bind := func(c *tctx) error {
		in, err := c.Bind[input]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%s/%d/%s/%s/%s/%v",
			in.Org, in.Page, in.Trace, in.Name, in.ID, in.IsAdmin))
	}
	r.GET("/{org}", bind)
	r.POST("/{org}", bind)

	req := httptest.NewRequest(http.MethodGet, "/acme?page=2&name=q&isadmin=on&IsAdmin=on", nil)
	req.Header.Set("X-Trace", "t1")
	if got, want := doReq(r, req).Body.String(), "acme/2/t1//acme/false"; got != want {
		t.Errorf("GET: body = %q, want %q", got, want)
	}

	req = httptest.NewRequest(http.MethodPost, "/acme?page=3", strings.NewReader(`{"name":"ann","id":"evil","IsAdmin":true}`))
	req.Header.Set(HeaderContentType, MIMEApplicationJSON)
	if got, want := doReq(r, req).Body.String(), "acme/3//ann/acme/true"; got != want {
		t.Errorf("POST JSON: body = %q, want %q", got, want)
	}

	rec := doBody(r, http.MethodPost, "/acme", MIMEApplicationForm, "name=ann&id=evil&IsAdmin=on&isadmin=on")
	if got, want := rec.Body.String(), "acme/0//ann/acme/false"; got != want {
		t.Errorf("POST form: body = %q, want %q", got, want)
	}

	if code := do(r, http.MethodGet, "/acme?page=x").Code; code != http.StatusBadRequest {
		t.Errorf("a malformed query: status = %d, want 400", code)
	}
}
