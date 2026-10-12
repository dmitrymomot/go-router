package router

import (
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
	h := http.Header{}
	h.Set(HeaderCacheControl, "x")
	h.Set(HeaderContentType, "image/png")
	h.Set("X-Other", "kept")
	ResetForError(h)
	if h.Get(HeaderCacheControl) != "" || h.Get("X-Other") != "kept" || h.Get(HeaderContentType) != "image/png" {
		t.Errorf("header = %v", h)
	}
	if h.Get(HeaderXContentTypeOptions) != "nosniff" {
		t.Errorf("no nosniff: %v", h)
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

func TestJSONMatchesMarshal(t *testing.T) {
	v := struct {
		A []int  `json:"a"`
		B string `json:"b"`
	}{[]int{1, 2}, "<&>"}
	want, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	r := newTestRouter()
	r.GET("/", func(c *tctx) error { return c.JSON(http.StatusOK, v) })
	rec := do(r, http.MethodGet, "/")
	if rec.Body.String() != string(want) {
		t.Errorf("body = %q, want %q", rec.Body, want)
	}
	if rec.Header().Get(HeaderContentLength) == "" {
		t.Errorf("no Content-Length")
	}
}

func TestJSONUnencodableWritesNothing(t *testing.T) {
	r := quietRouter()
	var got error
	r.GET("/", func(c *tctx) error {
		got = c.JSON(http.StatusOK, make(chan int))
		return nil
	})
	rec := do(r, http.MethodGet, "/")
	if StatusOf(got) != 500 || rec.Body.Len() != 0 || rec.Header().Get(HeaderContentType) != "" {
		t.Errorf("err %v, body %q, headers %v", got, rec.Body, rec.Header())
	}
}

func TestInvalidStatusWritesNothing(t *testing.T) {
	writers := map[string]func(c *tctx, status int) error{
		"Blob":       func(c *tctx, s int) error { return c.Blob(s, MIMETextPlain, []byte("x")) },
		"String":     func(c *tctx, s int) error { return c.String(s, "x") },
		"HTML":       func(c *tctx, s int) error { return c.HTML(s, "x") },
		"JSON":       func(c *tctx, s int) error { return c.JSON(s, 1) },
		"Stream":     func(c *tctx, s int) error { return c.Stream(s, MIMETextPlain, strings.NewReader("x")) },
		"NoContent":  func(c *tctx, s int) error { return c.NoContent(s) },
		"Attachment": func(c *tctx, s int) error { return c.Attachment(s, MIMETextPlain, "a.txt", []byte("x")) },
		"Inline":     func(c *tctx, s int) error { return c.Inline(s, MIMETextPlain, "a.txt", []byte("x")) },
		"Render":     func(c *tctx, s int) error { return c.Render(s, comp("x")) },
		"RenderStream": func(c *tctx, s int) error {
			return c.RenderStream(s, comp("x"))
		},
	}
	for name, w := range writers {
		for _, status := range []int{0, 99, 1000, -1} {
			t.Run(name, func(t *testing.T) {
				r := quietRouter()
				var got error
				r.GET("/", func(c *tctx) error {
					got = w(c, status)
					return nil
				})
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
				if got == nil || StatusOf(got) != 500 || !strings.Contains(got.Error(), "invalid status") {
					t.Errorf("status %d: err = %v", status, got)
					return
				}
				if rec.Body.Len() != 0 || rec.Header().Get(HeaderContentDisposition) != "" {
					t.Errorf("status %d wrote %q %v", status, rec.Body, rec.Header())
				}
			})
		}
	}
}

func TestInvalidStatusInAnErrorGivesABare500(t *testing.T) {
	r := quietRouter()
	r.GET("/", func(*tctx) error { return &HTTPError{Status: 1000, Message: "x"} })
	if rec := do(r, http.MethodGet, "/"); rec.Code != 500 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestIsLocalURL(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"/", true},
		{"/a/b?x=1#f", true},
		{"", false},
		{"a", false},
		{"//evil.com", false},
		{`/\evil.com`, false},
		{"https://evil.com", false},
		{"javascript:alert(1)", false},
		{"/a\tb", false},
		{"/a\nb", false},
		{"/a\x00b", false},
		{"/a\x7fb", false},
		{"/\t/evil.com", false},
	}
	for _, tt := range tests {
		if got := IsLocalURL(tt.in); got != tt.want {
			t.Errorf("IsLocalURL(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestLocalRedirect(t *testing.T) {
	r := quietRouter()
	r.GET("/", func(c *tctx) error { return c.LocalRedirect(http.StatusSeeOther, c.Query("next")) })
	rec := do(r, http.MethodGet, "/?next=/home")
	if rec.Code != 303 || rec.Header().Get(HeaderLocation) != "/home" {
		t.Errorf("status %d, location %q", rec.Code, rec.Header().Get(HeaderLocation))
	}
	for _, next := range []string{"//evil.com", "https://evil.com", "", "%5C%5Cevil.com"} {
		rec := do(r, http.MethodGet, "/?next="+next)
		if rec.Code != 400 || rec.Header().Get(HeaderLocation) != "" || !strings.Contains(rec.Body.String(), "redirect target is not a local path") {
			t.Errorf("next=%q: status %d, location %q, body %q", next, rec.Code, rec.Header().Get(HeaderLocation), rec.Body)
		}
	}
}

func TestETagOf(t *testing.T) {
	a, b := ETagOf([]byte("hello")), ETagOf([]byte("hello"))
	if a != b || ETagOf([]byte("world")) == a {
		t.Errorf("tags: %s %s", a, ETagOf([]byte("world")))
	}
	if len(a) != 24 || a[0] != '"' || a[23] != '"' {
		t.Errorf("tag = %s", a)
	}
}

func TestCheckETag(t *testing.T) {
	const tag = `"abc"`
	tests := []struct {
		name, method, inm string
		want              bool
	}{
		{"match", "GET", `"abc"`, true},
		{"head", "HEAD", `"abc"`, true},
		{"weak request", "GET", `W/"abc"`, true},
		{"list", "GET", `"x", W/"abc" ,"y"`, true},
		{"star", "GET", `*`, true},
		{"no match", "GET", `"abd"`, false},
		{"none", "GET", ``, false},
		{"prefix is not a match", "GET", `"ab"`, false},
		{"junk", "GET", `abc`, false},
		{"unterminated", "GET", `"abc`, false},
		{"post ignores", "POST", `"abc"`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestRouter()
			var got bool
			r.Handle(tt.method, "/", func(c *tctx) error {
				if got = c.CheckETag(tag); got {
					return nil
				}
				return c.String(http.StatusOK, "body")
			})
			headers := map[string]string{}
			if tt.inm != "" {
				headers[HeaderIfNoneMatch] = tt.inm
			}
			rec := doWithHeaders(r, tt.method, "/", headers)
			if got != tt.want {
				t.Fatalf("CheckETag = %v, want %v", got, tt.want)
			}
			if rec.Header().Get(HeaderETag) != tag {
				t.Errorf("ETag = %q", rec.Header().Get(HeaderETag))
			}
			if tt.want && (rec.Code != 304 || rec.Body.Len() != 0) {
				t.Errorf("status %d body %q", rec.Code, rec.Body)
			}
			if !tt.want && rec.Code != 200 {
				t.Errorf("status = %d", rec.Code)
			}
		})
	}
}

func TestTrailingSlash(t *testing.T) {
	r := newTestRouter()
	r.GET("/files/{path...}", func(c *tctx) error {
		if c.TrailingSlash() {
			return c.String(http.StatusOK, "slash")
		}
		return c.String(http.StatusOK, "plain")
	})
	for target, want := range map[string]string{"/files/a": "plain", "/files/a/": "slash"} {
		if got := do(r, http.MethodGet, target).Body.String(); got != want {
			t.Errorf("%s = %q, want %q", target, got, want)
		}
	}
}
