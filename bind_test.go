package router

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
	createUser
	TTL time.Duration `form:"ttl"`
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
		return c.Stringf(http.StatusOK, "%s/%d/%v/%v", in.Name, in.Age, in.Admin, in.TTL)
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
		Page  int      `query:"page"`
		Limit int      `query:"limit"`
		Sort  []string `query:"sort"`
		Since time.Time
	}

	r := newTestRouter()
	r.GET("/search", func(c *tctx) error {
		in, err := c.Bind[filter]()
		if err != nil {
			return err
		}
		return c.Stringf(http.StatusOK, "%d/%d/%v/%s",
			in.Page, in.Limit, in.Sort, in.Since.Format(time.RFC3339))
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
	if !strings.Contains(rec.Body.String(), `"field":"page"`) {
		t.Errorf("body does not name the field: %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cannot parse") {
		t.Errorf("body does not say what went wrong: %q", rec.Body.String())
	}
}

func TestBindQueryKeepsADecoderFaultOffTheWire(t *testing.T) {
	r := newTestRouter()
	r.GET("/s", func(c *tctx) error {
		_, err := c.BindQuery[[]string]()
		return err
	})

	rec := do(r, http.MethodGet, "/s?a=1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "decode target") {
		t.Errorf("body = %s, want no word of the decoder in it", rec.Body)
	}

	b := NewBase(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/s", nil))
	var target []string
	err := b.decodeInto(url.Values{"a": {"1"}}, &target, "query")
	he, ok := errors.AsType[*HTTPError](err)
	if !ok {
		t.Fatalf("decodeInto = %v, want an HTTPError", err)
	}
	if he.Message != http.StatusText(http.StatusBadRequest) {
		t.Errorf("Message = %q, want the standard text of the status", he.Message)
	}
	if he.Err == nil || !strings.Contains(he.Err.Error(), "decode target") {
		t.Errorf("Err = %v, want the decoder fault as the internal cause", he.Err)
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
		return c.Stringf(http.StatusOK, "%d", *in.Page)
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

func TestParamAsAndQueryAs(t *testing.T) {
	r := newTestRouter()
	r.GET("/users/{id}", func(c *tctx) error {
		id, err := c.ParamAs[int]("id")
		if err != nil {
			return err
		}
		limit := c.QueryAsDefault("limit", 25)
		return c.Stringf(http.StatusOK, "%d/%d", id, limit)
	})

	if got, want := do(r, http.MethodGet, "/users/9").Body.String(), "9/25"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if got, want := do(r, http.MethodGet, "/users/9?limit=5").Body.String(), "9/5"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if code := do(r, http.MethodGet, "/users/abc").Code; code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
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
	fields, err := decodeValues(url.Values{"offset": {"40"}, "q": {"go"}}, &got, "query", false)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(fields) != 0 {
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

func details(t *testing.T, rec *httptest.ResponseRecorder) []FieldError {
	t.Helper()
	var body struct {
		Status  int          `json:"status"`
		Error   string       `json:"error"`
		Details []FieldError `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body.Details
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
		return c.Stringf(http.StatusOK, "%s/%d", in.Name, in.Age)
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
		return c.Stringf(http.StatusOK, "%s/%s/%s", first.Name, second.Name, c.FormValue("name"))
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
		return c.Stringf(http.StatusOK, "%s/%d/%s", h.Filename, h.Size, content)
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
				return c.Stringf(http.StatusOK, "%d", len(content))
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
		return c.Stringf(http.StatusOK, "%s/%d", form.Value["name"][0], len(form.File["photo"]))
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
		return c.Stringf(http.StatusOK, "%s/%s/%s/%s",
			c.FormValue("name"), c.FormValue("role"),
			c.FormDefault("role", "user"), c.FormDefault("name", "anon"))
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
		return c.Stringf(http.StatusOK, "%q/%v", name, err != nil)
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
		return c.Stringf(http.StatusOK, "%v/%d", vals["tag"], len(vals))
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
		missing, err := c.FormAs[int]("missing")
		if err != nil {
			return err
		}
		return c.Stringf(http.StatusOK, "%d/%v/%d", age, ttl, missing)
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
		return c.Stringf(http.StatusOK, "%s/%s/%d", in.Org, in.Repo, in.Page)
	})

	if got, want := do(r, http.MethodGet, "/go/router").Body.String(), "go/router/0"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestBindPathReportsAParseError(t *testing.T) {
	type ref struct {
		ID int `param:"id"`
	}

	r := newTestRouter()
	r.GET("/users/{id}", func(c *tctx) error {
		_, err := c.BindPath[ref]()
		return err
	})

	rec := do(r, http.MethodGet, "/users/abc")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := details(t, rec); len(got) != 1 || got[0].Field != "id" {
		t.Errorf("details = %+v", got)
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
		return c.Stringf(http.StatusOK, "%s/%v/%q", in.RequestID, in.Wait, in.Missing)
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

func TestStrictBindFillsOnlyTaggedFields(t *testing.T) {
	type user struct {
		Name    string `form:"name"`
		IsAdmin bool
	}
	handler := func(c *tctx) error {
		in, err := c.BindForm[user]()
		if err != nil {
			return err
		}
		return c.Stringf(http.StatusOK, "%s/%v", in.Name, in.IsAdmin)
	}
	body := url.Values{"name": {"bo"}, "isadmin": {"true"}}

	loose := newTestRouter()
	loose.POST("/users", handler)
	if got, want := postForm(loose, "/users", body).Body.String(), "bo/true"; got != want {
		t.Errorf("loose body = %q, want %q", got, want)
	}

	strict := newTestRouter()
	strict.StrictBind(true)
	strict.POST("/users", handler)
	if got, want := postForm(strict, "/users", body).Body.String(), "bo/false"; got != want {
		t.Errorf("strict body = %q, want %q", got, want)
	}
}

func TestStrictBindFillsOnlyTaggedFieldsOfAJSONBody(t *testing.T) {
	type profile struct {
		City  string `json:"city"`
		Level int
	}
	type user struct {
		Name    string    `json:"name"`
		Profile profile   `json:"profile"`
		Friends []profile `json:"friends"`
		Since   time.Time `json:"since"`
		IsAdmin bool
	}
	handler := func(c *tctx) error {
		in, err := c.Bind[user]()
		if err != nil {
			return err
		}
		return c.Stringf(http.StatusOK, "%s/%v/%s/%d/%d/%d",
			in.Name, in.IsAdmin, in.Profile.City, in.Profile.Level,
			len(in.Friends), in.Since.Year())
	}
	body := `{"name":"bo","IsAdmin":true,"since":"2026-01-02T03:04:05Z",
		"profile":{"city":"lviv","Level":9},"friends":[{"city":"kyiv","Level":8}]}`

	loose := newTestRouter()
	loose.POST("/users", handler)
	if got, want := post(loose, "/users", MIMEApplicationJSON, body).Body.String(), "bo/true/lviv/9/1/2026"; got != want {
		t.Errorf("loose body = %q, want %q", got, want)
	}

	strict := newTestRouter()
	strict.StrictBind(true)
	strict.POST("/users", handler)
	if got, want := post(strict, "/users", MIMEApplicationJSON, body).Body.String(), "bo/false/lviv/0/1/2026"; got != want {
		t.Errorf("strict body = %q, want %q", got, want)
	}
}

type strictNode struct {
	Label string      `json:"label"`
	Next  *strictNode `json:"next"`
	Depth int
}

type strictCoin struct {
	Amount int
	Code   string
}

func (c *strictCoin) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	_, err := fmt.Sscanf(s, "%d %s", &c.Amount, &c.Code)
	return err
}

func TestStripUntaggedWalksTheValuesItHolds(t *testing.T) {
	type in struct {
		Node  *strictNode           `json:"node"`
		Items map[string]strictNode `json:"items"`
		Names map[string]string     `json:"names"`
		Price strictCoin            `json:"price"`
		Loose string
	}
	v := in{
		Node:  &strictNode{Label: "a", Depth: 3, Next: &strictNode{Label: "b", Depth: 4}},
		Items: map[string]strictNode{"x": {Label: "c", Depth: 5}},
		Names: map[string]string{"k": "v"},
		Price: strictCoin{Amount: 12, Code: "EUR"},
		Loose: "reached",
	}
	stripUntagged(reflect.ValueOf(&v).Elem())

	if v.Loose != "" {
		t.Errorf("Loose = %q, want the untagged field blanked", v.Loose)
	}
	if v.Node.Label != "a" || v.Node.Depth != 0 {
		t.Errorf("Node = %+v, want the label kept and the depth blanked", *v.Node)
	}
	if v.Node.Next.Depth != 0 {
		t.Errorf("Node.Next = %+v, want the walk to reach through a pointer", *v.Node.Next)
	}
	if got := v.Items["x"]; got.Label != "c" || got.Depth != 0 {
		t.Errorf("Items[x] = %+v, want the walk to reach a map value", got)
	}
	if v.Names["k"] != "v" {
		t.Errorf("Names = %v, want a map of strings left alone", v.Names)
	}
	if want := (strictCoin{Amount: 12, Code: "EUR"}); v.Price != want {
		t.Errorf("Price = %+v, want %+v: a type that decodes itself keeps what it read", v.Price, want)
	}
}

func TestStrictBindKeepsEmbeddedTaggedFields(t *testing.T) {
	type page struct {
		Offset int `query:"offset"`
	}
	type filter struct {
		page
		Term string `query:"q"`
	}

	r := newTestRouter()
	r.StrictBind(true)
	r.GET("/search", func(c *tctx) error {
		in, err := c.BindQuery[filter]()
		if err != nil {
			return err
		}
		return c.Stringf(http.StatusOK, "%d/%s", in.Offset, in.Term)
	})

	if got, want := do(r, http.MethodGet, "/search?offset=40&q=go").Body.String(), "40/go"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestQueryAsOK(t *testing.T) {
	r := newTestRouter()
	r.GET("/search", func(c *tctx) error {
		page, ok, err := c.QueryAsOK[int]("page")
		if err != nil {
			return err
		}
		return c.Stringf(http.StatusOK, "%d/%v", page, ok)
	})

	for _, tt := range []struct {
		target string
		want   string
		code   int
	}{
		{target: "/search?page=2", want: "2/true", code: http.StatusOK},
		{target: "/search?page=", want: "0/true", code: http.StatusOK},
		{target: "/search", want: "0/false", code: http.StatusOK},
		{target: "/search?page=abc", code: http.StatusBadRequest},
	} {
		t.Run(tt.target, func(t *testing.T) {
			rec := do(r, http.MethodGet, tt.target)
			if rec.Code != tt.code {
				t.Fatalf("status = %d, want %d", rec.Code, tt.code)
			}
			if tt.want != "" && rec.Code == http.StatusOK && rec.Body.String() != tt.want {
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
		return c.Stringf(http.StatusOK, "%v/%d", ids, len(ids))
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
		return c.Stringf(http.StatusOK, "%d/%d",
			c.ParamAsDefault("id", 7), c.ParamAsDefault("missing", 3))
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
	empty, err := ParseValue[int]("")
	if err != nil || empty != 0 {
		t.Errorf("ParseValue of an empty string = %d, %v", empty, err)
	}
	unset, err := ParseValue[*int]("")
	if err != nil || unset != nil {
		t.Errorf("ParseValue[*int] of an empty string = %v, %v", unset, err)
	}
	when, err := ParseValue[time.Time]("2026-01-02T03:04:05Z")
	if err != nil || when.Year() != 2026 {
		t.Errorf("ParseValue = %v, %v", when, err)
	}
}

func TestParseValueDefault(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want time.Duration
	}{
		{in: "90s", want: 90 * time.Second},
		{in: "", want: time.Minute},
		{in: "nope", want: time.Minute},
	} {
		t.Run(tt.in, func(t *testing.T) {
			if got := ParseValueDefault(tt.in, time.Minute); got != tt.want {
				t.Errorf("ParseValueDefault(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestFieldErrorsReachesThroughAnErrorTree(t *testing.T) {
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fieldErrors(tt.err); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("fieldErrors = %+v, want %+v", got, tt.want)
			}
		})
	}
}
