package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func tokenContext(req *http.Request) router.Context {
	return router.NewBase(httptest.NewRecorder(), req)
}

func postForm(target string, values url.Values) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	req.Header.Set(router.HeaderContentType, router.MIMEApplicationForm)
	return req
}

func mustPanicContaining(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		switch v := recover().(type) {
		case nil:
			t.Errorf("no panic, want one that reads %q", want)
		case string:
			if !strings.Contains(v, want) {
				t.Errorf("panic = %q, want one that holds %q", v, want)
			}
		default:
			t.Errorf("panic = %v of type %T, want a string", v, v)
		}
	}()
	fn()
}

func TestFromHeaderCutsTheSchemePrefix(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		values []string
		want   []string
	}{
		{"a bearer token", "Bearer ", []string{"Bearer abc"}, []string{"abc"}},
		{"the scheme in any case", "Bearer ", []string{"bEaReR abc"}, []string{"abc"}},
		{"another scheme", "Bearer ", []string{"Basic abc"}, nil},
		{"nothing after the scheme", "Bearer ", []string{"Bearer "}, nil},
		{"the scheme alone", "Bearer ", []string{"Bearer"}, nil},
		{"no prefix to cut", "", []string{"abc"}, []string{"abc"}},
		{"an empty value", "", []string{""}, nil},
		{"a header that repeats", "Bearer ", []string{"Bearer a", "Bearer b"}, []string{"a", "b"}},
		{"no header at all", "Bearer ", nil, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			for _, v := range tt.values {
				req.Header.Add(router.HeaderAuthorization, v)
			}
			got := middleware.FromHeader("authorization", tt.prefix)(tokenContext(req))
			if !slices.Equal(got, tt.want) {
				t.Errorf("tokens = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFromQueryReadsTheParameter(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   []string
	}{
		{"one value", "/?token=abc", []string{"abc"}},
		{"an empty value", "/?token=", nil},
		{"a parameter that repeats", "/?token=a&token=b", []string{"a", "b"}},
		{"another parameter", "/?other=abc", nil},
		{"no query at all", "/", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			got := middleware.FromQuery("token")(tokenContext(req))
			if !slices.Equal(got, tt.want) {
				t.Errorf("tokens = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFromCookieReadsTheCookie(t *testing.T) {
	tests := []struct {
		name    string
		cookies []string
		want    []string
	}{
		{"one cookie", []string{"session=abc"}, []string{"abc"}},
		{"an empty cookie", []string{"session="}, nil},
		{"another cookie", []string{"other=abc"}, nil},
		{"the cookie twice", []string{"session=a", "session=b"}, []string{"a", "b"}},
		{"no cookie at all", nil, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			for _, c := range tt.cookies {
				req.Header.Add(router.HeaderCookie, c)
			}
			got := middleware.FromCookie("session")(tokenContext(req))
			if !slices.Equal(got, tt.want) {
				t.Errorf("tokens = %q, want %q", got, tt.want)
			}
		})
	}
}

func formSourceRouter(maxBody int64) *router.Router[*appContext] {
	r := newRouter()
	if maxBody > 0 {
		r.MaxBodyBytes(maxBody)
	}
	r.POST("/", func(c *appContext) error {
		token := strings.Join(middleware.FromForm("_csrf")(c), ",")
		return c.String(http.StatusOK, token+"|"+c.FormValue("title"))
	})
	return r
}

func TestFromFormLeavesTheBodyReadable(t *testing.T) {
	r := formSourceRouter(0)

	rec := do(r, postForm("/", url.Values{"_csrf": {"tok"}, "title": {"hello"}}))
	if got := rec.Body.String(); got != "tok|hello" {
		t.Errorf("answer = %q, want %q", got, "tok|hello")
	}
}

func TestFromFormReadsTheBodyAndNotTheQuery(t *testing.T) {
	r := formSourceRouter(0)

	rec := do(r, postForm("/?_csrf=forged", url.Values{"title": {"hello"}}))
	if got := rec.Body.String(); got != "|hello" {
		t.Errorf("answer = %q, want %q", got, "|hello")
	}
}

func TestFromFormObeysTheBodyLimit(t *testing.T) {
	r := formSourceRouter(16)

	rec := do(r, postForm("/", url.Values{"_csrf": {strings.Repeat("k", 64)}}))
	if got := rec.Body.String(); got != "|" {
		t.Errorf("answer = %q, want no token and no title", got)
	}
}

func TestFromFormReadsAMultipartBody(t *testing.T) {
	r := formSourceRouter(0)

	body := strings.Join([]string{
		"--b", `Content-Disposition: form-data; name="_csrf"`, "", "tok",
		"--b", `Content-Disposition: form-data; name="title"`, "", "hello",
		"--b--", "",
	}, "\r\n")
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(router.HeaderContentType, `multipart/form-data; boundary=b`)

	if got := do(r, req).Body.String(); got != "tok|hello" {
		t.Errorf("answer = %q, want %q", got, "tok|hello")
	}
}

func TestTokenSourceCapPanics(t *testing.T) {
	validator := func(*appContext, string) (bool, error) { return true, nil }
	tooMany := make([]middleware.TokenSource, middleware.MaxTokenSources+1)
	for i := range tooMany {
		tooMany[i] = middleware.FromQuery("token")
	}

	t.Run("KeyAuthConfig", func(t *testing.T) {
		mustPanicContaining(t, "at most 20", func() {
			middleware.KeyAuthWithConfig(middleware.KeyAuthConfig[*appContext]{
				Sources:   tooMany,
				Validator: validator,
			})
		})
	})

	t.Run("CSRFConfig", func(t *testing.T) {
		mustPanicContaining(t, "at most 20", func() {
			middleware.CSRFWithConfig[*appContext](middleware.CSRFConfig{TokenSources: tooMany})
		})
	})

	t.Run("the cap itself passes", func(t *testing.T) {
		middleware.CSRFWithConfig[*appContext](middleware.CSRFConfig{
			TokenSources: tooMany[:middleware.MaxTokenSources],
		})
	})
}

func TestTokenSourceNilPanics(t *testing.T) {
	mustPanicContaining(t, "nil token source at index 1", func() {
		middleware.CSRFWithConfig[*appContext](middleware.CSRFConfig{
			TokenSources: []middleware.TokenSource{middleware.FromQuery("token"), nil},
		})
	})
}

func TestFromFormReadsTheMethodsThatNetHTTPParses(t *testing.T) {
	r := newRouter()
	h := func(c *appContext) error {
		return c.String(http.StatusOK, strings.Join(middleware.FromForm("_csrf")(c), ","))
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		r.Handle(m, "/", h)
	}

	multipart := strings.Join([]string{
		"--b", `Content-Disposition: form-data; name="_csrf"`, "", "tok", "--b--", "",
	}, "\r\n")

	tests := []struct {
		method string
		want   string
	}{
		{http.MethodPost, "tok"},
		{http.MethodPut, "tok"},
		{http.MethodPatch, "tok"},
		{http.MethodDelete, ""},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			body := url.Values{"_csrf": {"tok"}}.Encode()
			req := httptest.NewRequest(tt.method, "/", strings.NewReader(body))
			req.Header.Set(router.HeaderContentType, router.MIMEApplicationForm)
			if got := do(r, req).Body.String(); got != tt.want {
				t.Errorf("urlencoded token = %q, want %q", got, tt.want)
			}

			req = httptest.NewRequest(tt.method, "/", strings.NewReader(multipart))
			req.Header.Set(router.HeaderContentType, `multipart/form-data; boundary=b`)
			if got := do(r, req).Body.String(); got != "tok" {
				t.Errorf("multipart token = %q, want %q", got, "tok")
			}
		})
	}
}
