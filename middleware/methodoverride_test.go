package middleware_test

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func methodOverrideRouter(cfg middleware.MethodOverrideConfig) *router.Router[*appContext] {
	r := newRouter()
	r.Pre(middleware.MethodOverrideWithConfig[*appContext](cfg))
	echo := func(c *appContext) error { return c.String(http.StatusOK, c.Request().Method) }
	r.GET("/posts/{id}", echo)
	r.POST("/posts/{id}", echo)
	r.PUT("/posts/{id}", echo)
	r.PATCH("/posts/{id}", echo)
	r.DELETE("/posts/{id}", echo)
	return r
}

func methodOverridePost(target, override string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, nil)
	if override != "" {
		req.Header.Set(router.HeaderXHTTPMethodOverride, override)
	}
	return req
}

func TestMethodOverrideUpgradesAPost(t *testing.T) {
	tests := []struct {
		name     string
		override string
		want     string
	}{
		{name: "delete", override: "DELETE", want: http.MethodDelete},
		{name: "put", override: "PUT", want: http.MethodPut},
		{name: "patch", override: "PATCH", want: http.MethodPatch},
		{name: "in lower case", override: "delete", want: http.MethodDelete},
		{name: "with spaces around it", override: " delete ", want: http.MethodDelete},
		{name: "a downgrade to get", override: "GET", want: http.MethodPost},
		{name: "a downgrade to head", override: "HEAD", want: http.MethodPost},
		{name: "a method that is not one", override: "TELEPORT", want: http.MethodPost},
		{name: "no header at all", override: "", want: http.MethodPost},
	}

	r := methodOverrideRouter(middleware.MethodOverrideConfig{})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(r, methodOverridePost("/posts/7", tc.override))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if rec.Body.String() != tc.want {
				t.Errorf("method = %q, want %q", rec.Body.String(), tc.want)
			}
		})
	}
}

func TestMethodOverrideIgnoresEveryOtherMethod(t *testing.T) {
	r := methodOverrideRouter(middleware.MethodOverrideConfig{})

	req := httptest.NewRequest(http.MethodGet, "/posts/7", nil)
	req.Header.Set(router.HeaderXHTTPMethodOverride, "DELETE")
	if got := do(r, req).Body.String(); got != http.MethodGet {
		t.Errorf("method = %q, want GET: only a POST is upgraded", got)
	}
}

func TestMethodOverridePlainFormReadsTheHeader(t *testing.T) {
	r := newRouter()
	r.Pre(middleware.MethodOverride[*appContext])
	r.DELETE("/posts/{id}", func(c *appContext) error {
		return c.String(http.StatusOK, c.Param("id"))
	})

	rec := do(r, methodOverridePost("/posts/7", "DELETE"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "7" {
		t.Errorf("id = %q, want 7", rec.Body.String())
	}
}

func TestMethodOverrideFromFormLeavesTheBodyReadable(t *testing.T) {
	r := newRouter()
	r.Pre(middleware.MethodOverrideWithConfig[*appContext](middleware.MethodOverrideConfig{
		Getter: middleware.MethodFromForm("_method"),
	}))
	r.DELETE("/posts/{id}", func(c *appContext) error {
		return c.Stringf(http.StatusOK, "%s %s", c.Request().Method, c.FormValue("title"))
	})

	req := httptest.NewRequest(http.MethodPost, "/posts/7",
		strings.NewReader("_method=DELETE&title=the+title"))
	req.Header.Set(router.HeaderContentType, router.MIMEApplicationForm)

	rec := do(r, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != "DELETE the title" {
		t.Errorf("the handler saw %q, want the method and the field beside it", got)
	}
}

func TestMethodOverrideFromQuery(t *testing.T) {
	r := methodOverrideRouter(middleware.MethodOverrideConfig{
		Getter: middleware.MethodFromQuery("_method"),
	})

	req := httptest.NewRequest(http.MethodPost, "/posts/7?_method=PATCH", nil)
	if got := do(r, req).Body.String(); got != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", got)
	}
}

func TestMethodOverrideFromHeaderReadsTheNamedHeader(t *testing.T) {
	r := methodOverrideRouter(middleware.MethodOverrideConfig{
		Getter: middleware.MethodFromHeader("X-Method"),
	})

	req := httptest.NewRequest(http.MethodPost, "/posts/7", nil)
	req.Header.Set("X-Method", "PUT")
	req.Header.Set(router.HeaderXHTTPMethodOverride, "DELETE")
	if got := do(r, req).Body.String(); got != http.MethodPut {
		t.Errorf("method = %q, want PUT: the configured header decides", got)
	}
}

func TestMethodOverrideSkip(t *testing.T) {
	r := methodOverrideRouter(middleware.MethodOverrideConfig{
		Skip: skipPath("/posts/7"),
	})

	if got := do(r, methodOverridePost("/posts/7", "DELETE")).Body.String(); got != http.MethodPost {
		t.Errorf("method = %q, want POST", got)
	}
}

type countingReader struct {
	r    io.Reader
	read int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	return n, err
}

func multipartOverride(t *testing.T, method string, filler int) (body []byte, contentType string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("_method", method); err != nil {
		t.Fatalf("write the field: %v", err)
	}
	part, err := w.CreateFormFile("upload", "big.bin")
	if err != nil {
		t.Fatalf("create the file part: %v", err)
	}
	if _, err := part.Write(make([]byte, filler)); err != nil {
		t.Fatalf("write the file part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close the form: %v", err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

func methodFromFormRouter() *router.Router[*appContext] {
	r := newRouter()
	r.Pre(middleware.MethodOverrideWithConfig[*appContext](middleware.MethodOverrideConfig{
		Getter: middleware.MethodFromForm("_method"),
	}))
	r.POST("/posts/7", func(c *appContext) error {
		if _, err := c.FormValues(); err != nil {
			return err
		}
		return c.String(http.StatusOK, "post")
	})
	r.DELETE("/posts/7", func(c *appContext) error { return c.String(http.StatusOK, "delete") })
	return r
}

func TestMethodFromFormBoundsTheBodyItParses(t *testing.T) {
	body, contentType := multipartOverride(t, http.MethodDelete, 1<<20)

	r := methodFromFormRouter()
	r.MaxBodyBytes(4 << 10)

	counted := &countingReader{r: bytes.NewReader(body)}
	req := httptest.NewRequest(http.MethodPost, "/posts/7", counted)
	req.Header.Set(router.HeaderContentType, contentType)
	rec := do(r, req)

	if counted.read > 64<<10 {
		t.Errorf("the parse pulled %d bytes off the wire, against a limit of %d", counted.read, 4<<10)
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413: a body past the cap answers, rather than passing as a form with no method in it", rec.Code)
	}
}

func TestMethodFromFormReadsAMultipartFormUnderTheCap(t *testing.T) {
	body, contentType := multipartOverride(t, http.MethodDelete, 512)

	r := methodFromFormRouter()
	r.MaxBodyBytes(64 << 10)

	req := httptest.NewRequest(http.MethodPost, "/posts/7", &countingReader{r: bytes.NewReader(body)})
	req.Header.Set(router.HeaderContentType, contentType)

	rec := do(r, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec.Body.String() != "delete" {
		t.Errorf("the request reached %q, want the DELETE route", rec.Body)
	}
}

func TestMethodOverrideAndTheFormItLeavesUnparsed(t *testing.T) {
	tests := []struct {
		name   string
		getter func(router.Context) string
		want   string
	}{
		{"from the query", middleware.MethodFromQuery("_method"), "name= csrf="},
		{"from a header", middleware.MethodFromHeader("X-Method"), "name= csrf="},
		{"from the form", middleware.MethodFromForm("_method"), "name=ada csrf=t0ken"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRouter()
			r.Pre(middleware.MethodOverrideWithConfig[*appContext](
				middleware.MethodOverrideConfig{Getter: tt.getter}))
			r.DELETE("/posts/7", func(c *appContext) error {
				return c.String(http.StatusOK, "name="+c.FormValue("name")+
					" csrf="+strings.Join(middleware.FromForm("csrf")(c), ""))
			})

			req := httptest.NewRequest(http.MethodPost, "/posts/7?_method=DELETE",
				strings.NewReader("name=ada&csrf=t0ken&_method=DELETE"))
			req.Header.Set(router.HeaderContentType, router.MIMEApplicationForm)
			req.Header.Set("X-Method", http.MethodDelete)

			rec := do(r, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: the override never reached the route",
					rec.Code, http.StatusOK)
			}
			if got := rec.Body.String(); got != tt.want {
				t.Errorf("the handler read %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMethodOverrideKeepsAMultipartFormReadable(t *testing.T) {
	r := newRouter()
	r.Pre(middleware.MethodOverrideWithConfig[*appContext](
		middleware.MethodOverrideConfig{Getter: middleware.MethodFromQuery("_method")}))
	r.DELETE("/posts/7", func(c *appContext) error {
		return c.String(http.StatusOK, "csrf="+strings.Join(middleware.FromForm("csrf")(c), ""))
	})

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("csrf", "t0ken"); err != nil {
		t.Fatalf("write the field: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close the writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/posts/7?_method=DELETE", &body)
	req.Header.Set(router.HeaderContentType, mw.FormDataContentType())

	if got := do(r, req).Body.String(); got != "csrf=t0ken" {
		t.Errorf("the handler read %q, want %q", got, "csrf=t0ken")
	}
}
