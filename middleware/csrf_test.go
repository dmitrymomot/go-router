package middleware_test

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func csrfRouter(cfg middleware.CSRFConfig) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.CSRFWithConfig[*appContext](cfg))
	h := func(c *appContext) error {
		return c.String(http.StatusOK, middleware.CSRFTokenFrom(c))
	}
	r.GET("/", h)
	r.POST("/", h)
	return r
}

func csrfCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == middleware.DefaultCSRFCookieName {
			return c
		}
	}
	t.Fatal("the answer set no token cookie")
	return nil
}

func csrfSession(t *testing.T, r http.Handler) (string, *http.Cookie) {
	t.Helper()
	rec := get(r, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	return rec.Body.String(), csrfCookie(t, rec)
}

func withCookie(req *http.Request, c *http.Cookie) *http.Request {
	req.AddCookie(c)
	return req
}

func TestCSRFIssuesATokenOnASafeRequest(t *testing.T) {
	r := csrfRouter(middleware.CSRFConfig{})

	rec := get(r, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	token := rec.Body.String()
	if token == "" {
		t.Fatal("no token on the context")
	}
	if got := csrfCookie(t, rec).Value; got != token {
		t.Errorf("cookie = %q, want the token that the handler read", got)
	}

	if got := rec.Header().Values(router.HeaderVary); !containsFold(got, router.HeaderCookie) {
		t.Errorf("vary = %q, want it to name Cookie", got)
	}

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("the token is not raw URL base64: %v", err)
	}
	if len(raw) != 32 {
		t.Errorf("the token carries %d bytes, want 32", len(raw))
	}
}

func TestCSRFTokensDiffer(t *testing.T) {
	r := csrfRouter(middleware.CSRFConfig{})

	seen := make(map[string]bool, 64)
	for range 64 {
		token := get(r, "/").Body.String()
		if seen[token] {
			t.Fatalf("the middleware issued %q twice", token)
		}
		seen[token] = true
	}
}

func TestCSRFKeepsTheTokenOfTheCookie(t *testing.T) {
	r := csrfRouter(middleware.CSRFConfig{})
	token, cookie := csrfSession(t, r)

	rec := do(r, withCookie(httptest.NewRequest(http.MethodGet, "/", nil), cookie))
	if got := rec.Body.String(); got != token {
		t.Errorf("token = %q, want the one that the cookie carries", got)
	}
	if got := rec.Result().Cookies(); len(got) != 0 {
		t.Errorf("the answer set %d cookies, want none for a request that carries one", len(got))
	}
}

func TestCSRFSafeMethodsSkipValidation(t *testing.T) {
	r := newRouter()
	r.Use(middleware.CSRF[*appContext])
	h := func(c *appContext) error { return c.String(http.StatusOK, "ok") }
	r.GET("/", h)
	r.Handle(http.MethodTrace, "/", h)
	r.POST("/", h)

	tests := []struct {
		method string
		want   int
	}{
		{http.MethodGet, http.StatusOK},
		{http.MethodHead, http.StatusOK},
		{http.MethodTrace, http.StatusOK},
		{http.MethodOptions, http.StatusNoContent},
		{http.MethodPost, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			rec := do(r, httptest.NewRequest(tt.method, "/", nil))
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestCSRFAcceptsTheTokenFromTheHeader(t *testing.T) {
	r := csrfRouter(middleware.CSRFConfig{})
	token, cookie := csrfSession(t, r)

	req := withCookie(httptest.NewRequest(http.MethodPost, "/", nil), cookie)
	req.Header.Set(router.HeaderXCSRFToken, token)
	if rec := do(r, req); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestCSRFAcceptsTheTokenFromTheForm(t *testing.T) {
	r := newRouter()
	r.Use(middleware.CSRF[*appContext])
	r.GET("/", func(c *appContext) error {
		return c.String(http.StatusOK, middleware.CSRFTokenFrom(c))
	})
	r.POST("/", func(c *appContext) error {
		return c.String(http.StatusOK, c.FormValue("title"))
	})
	token, cookie := csrfSession(t, r)

	body := url.Values{middleware.DefaultCSRFFormField: {token}, "title": {"hello"}}
	rec := do(r, withCookie(postForm("/", body), cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != "hello" {
		t.Errorf("title = %q, want the body that the handler read after the check", got)
	}
}

func TestCSRFRefusesAnUnsafeRequest(t *testing.T) {
	r := csrfRouter(middleware.CSRFConfig{})
	token, cookie := csrfSession(t, r)

	tests := []struct {
		name    string
		request func() *http.Request
	}{
		{
			name: "no token at all",
			request: func() *http.Request {
				return withCookie(httptest.NewRequest(http.MethodPost, "/", nil), cookie)
			},
		},
		{
			name: "a token that does not match",
			request: func() *http.Request {
				req := withCookie(httptest.NewRequest(http.MethodPost, "/", nil), cookie)
				req.Header.Set(router.HeaderXCSRFToken, strings.Repeat("x", len(token)))
				return req
			},
		},
		{
			name: "a token and no cookie",
			request: func() *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/", nil)
				req.Header.Set(router.HeaderXCSRFToken, token)
				return req
			},
		},
		{
			name: "a token in the query string",
			request: func() *http.Request {
				return withCookie(httptest.NewRequest(http.MethodPost, "/?_csrf="+token, nil), cookie)
			},
		},
		{
			name: "two cookies of the name",
			request: func() *http.Request {
				req := withCookie(httptest.NewRequest(http.MethodPost, "/", nil), cookie)
				req.AddCookie(&http.Cookie{Name: middleware.DefaultCSRFCookieName, Value: "forged"})
				req.Header.Set(router.HeaderXCSRFToken, "forged")
				return req
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := do(r, tt.request()); rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
		})
	}
}

func TestCSRFSecFetchSiteDecidesFirst(t *testing.T) {
	r := csrfRouter(middleware.CSRFConfig{})
	token, cookie := csrfSession(t, r)

	tests := []struct {
		name  string
		value string
		token bool
		want  int
	}{
		{"the site asked itself", "same-origin", false, http.StatusOK},
		{"the user asked", "none", false, http.StatusOK},
		{"another site asked", "cross-site", true, http.StatusForbidden},
		{"a sibling subdomain asked", "same-site", true, http.StatusForbidden},
		{"no metadata and a token", "", true, http.StatusOK},
		{"no metadata and no token", "", false, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := withCookie(httptest.NewRequest(http.MethodPost, "/", nil), cookie)
			if tt.value != "" {
				req.Header.Set(router.HeaderSecFetchSite, tt.value)
			}
			if tt.token {
				req.Header.Set(router.HeaderXCSRFToken, token)
			}
			if rec := do(r, req); rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestCSRFTrustedOriginPasses(t *testing.T) {
	r := csrfRouter(middleware.CSRFConfig{
		TrustedOrigins: []string{"https://APP.example", "http://localhost:3000"},
	})
	_, cookie := csrfSession(t, r)

	tests := []struct {
		name   string
		origin string
		want   int
	}{
		{"a trusted origin", "https://app.example", http.StatusOK},
		{"a trusted origin with a port", "http://localhost:3000", http.StatusOK},
		{"the host in another case", "https://App.Example", http.StatusOK},
		{"another scheme", "http://app.example", http.StatusForbidden},
		{"another port", "http://localhost:3001", http.StatusForbidden},
		{"an origin no one trusts", "https://evil.example", http.StatusForbidden},
		{"no origin at all", "", http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := withCookie(httptest.NewRequest(http.MethodPost, "/", nil), cookie)
			req.Header.Set(router.HeaderSecFetchSite, "cross-site")
			if tt.origin != "" {
				req.Header.Set(router.HeaderOrigin, tt.origin)
			}
			if rec := do(r, req); rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestCSRFMalformedTrustedOriginPanics(t *testing.T) {
	tests := []struct {
		name   string
		origin string
	}{
		{"a host alone", "app.example"},
		{"a trailing slash", "https://app.example/"},
		{"a path", "https://app.example/app"},
		{"a query", "https://app.example?a=b"},
		{"a fragment", "https://app.example#a"},
		{"a user", "https://ops@app.example"},
		{"a scheme alone", "https://"},
		{"nothing", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustPanicContaining(t, "CSRFConfig.TrustedOrigins", func() {
				middleware.CSRFWithConfig[*appContext](middleware.CSRFConfig{
					TrustedOrigins: []string{tt.origin},
				})
			})
		})
	}

	t.Run("an origin that is one", func(t *testing.T) {
		middleware.CSRFWithConfig[*appContext](middleware.CSRFConfig{
			TrustedOrigins: []string{"https://app.example", "http://localhost:3000"},
		})
	})
}

func TestCSRFAllowSecFetchSiteOfItsOwn(t *testing.T) {
	t.Run("it accepts the request", func(t *testing.T) {
		r := csrfRouter(middleware.CSRFConfig{
			AllowSecFetchSite: func(router.Context) (bool, error) { return true, nil },
		})
		if rec := do(r, httptest.NewRequest(http.MethodPost, "/", nil)); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("it refuses the request", func(t *testing.T) {
		r := csrfRouter(middleware.CSRFConfig{
			AllowSecFetchSite: func(router.Context) (bool, error) {
				return false, router.ErrTooManyRequests
			},
		})
		token, cookie := csrfSession(t, r)

		req := withCookie(httptest.NewRequest(http.MethodPost, "/", nil), cookie)
		req.Header.Set(router.HeaderXCSRFToken, token)
		if rec := do(r, req); rec.Code != http.StatusTooManyRequests {
			t.Errorf("status = %d, want 429", rec.Code)
		}
	})

	t.Run("it hands the request to the token", func(t *testing.T) {
		r := csrfRouter(middleware.CSRFConfig{
			AllowSecFetchSite: func(router.Context) (bool, error) { return false, nil },
		})
		token, cookie := csrfSession(t, r)

		req := withCookie(httptest.NewRequest(http.MethodPost, "/", nil), cookie)
		req.Header.Set(router.HeaderXCSRFToken, token)
		if rec := do(r, req); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})
}

func TestCSRFCookieAttributes(t *testing.T) {
	t.Run("the defaults", func(t *testing.T) {
		cookie := csrfCookie(t, get(csrfRouter(middleware.CSRFConfig{}), "/"))

		if cookie.Path != "/" {
			t.Errorf("path = %q, want the whole site", cookie.Path)
		}
		if want := int(middleware.DefaultCSRFCookieMaxAge.Seconds()); cookie.MaxAge != want {
			t.Errorf("max age = %d, want %d", cookie.MaxAge, want)
		}
		if cookie.Secure || cookie.HttpOnly {
			t.Errorf("secure = %v, http only = %v, want both off by default",
				cookie.Secure, cookie.HttpOnly)
		}
	})

	t.Run("a config of its own", func(t *testing.T) {
		cookie := csrfCookie(t, get(csrfRouter(middleware.CSRFConfig{
			CookieName:     "_csrf",
			CookiePath:     "/app",
			CookieDomain:   "app.example",
			CookieMaxAge:   2 * time.Hour,
			CookieSecure:   true,
			CookieHTTPOnly: true,
			CookieSameSite: http.SameSiteStrictMode,
		}), "/"))

		switch {
		case cookie.Path != "/app":
			t.Errorf("path = %q, want %q", cookie.Path, "/app")
		case cookie.Domain != "app.example":
			t.Errorf("domain = %q, want %q", cookie.Domain, "app.example")
		case cookie.MaxAge != 7200:
			t.Errorf("max age = %d, want 7200", cookie.MaxAge)
		case !cookie.Secure || !cookie.HttpOnly:
			t.Errorf("secure = %v, http only = %v, want both on", cookie.Secure, cookie.HttpOnly)
		case cookie.SameSite != http.SameSiteStrictMode:
			t.Errorf("same site = %v, want strict", cookie.SameSite)
		}
	})

	t.Run("SameSite None forces Secure", func(t *testing.T) {
		cookie := csrfCookie(t, get(csrfRouter(middleware.CSRFConfig{
			CookieSameSite: http.SameSiteNoneMode,
		}), "/"))

		if !cookie.Secure {
			t.Error("the cookie is not secure")
		}
	})

	t.Run("a cookie name of its own", func(t *testing.T) {
		r := csrfRouter(middleware.CSRFConfig{CookieName: "session_csrf"})

		rec := get(r, "/")
		cookies := rec.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != "session_csrf" {
			t.Fatalf("cookies = %v, want one named session_csrf", cookies)
		}
		if cookies[0].Value != rec.Body.String() {
			t.Error("the cookie does not carry the token that the handler read")
		}
	})
}

func TestCSRFTokenReachesAComponent(t *testing.T) {
	r := newRouter()
	r.Use(middleware.CSRF[*appContext])
	r.GET("/", func(c *appContext) error {
		return c.Render(http.StatusOK, router.ComponentFunc(
			func(ctx context.Context, w io.Writer) error {
				b, ok := router.FromContext(ctx)
				if !ok {
					return errors.New("the component received no request state")
				}
				_, err := io.WriteString(w,
					`<input type="hidden" name="_csrf" value="`+middleware.CSRFTokenFrom(b)+`">`)
				return err
			}))
	})

	rec := get(r, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	want := `<input type="hidden" name="_csrf" value="` + csrfCookie(t, rec).Value + `">`
	if got := rec.Body.String(); got != want {
		t.Errorf("page = %q, want %q", got, want)
	}
}

func TestCSRFCopiesConfiguredTokenSources(t *testing.T) {
	sources := []middleware.TokenSource{middleware.FromHeader("X-Original-CSRF", "")}
	r := csrfRouter(middleware.CSRFConfig{TokenSources: sources})
	sources[0] = middleware.FromHeader("X-Replaced-CSRF", "")

	token, cookie := csrfSession(t, r)
	req := withCookie(httptest.NewRequest(http.MethodPost, "/", nil), cookie)
	req.Header.Set("X-Original-CSRF", token)
	if rec := do(r, req); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 after the caller mutated its source slice", rec.Code)
	}
}

func TestCSRFSkip(t *testing.T) {
	r := csrfRouter(middleware.CSRFConfig{Skip: skipPath("/")})

	rec := do(r, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Result().Cookies(); len(got) != 0 {
		t.Errorf("the answer set %d cookies, want none", len(got))
	}
	if got := rec.Header().Values(router.HeaderVary); containsFold(got, router.HeaderCookie) {
		t.Errorf("vary = %q, want none", got)
	}
}

func containsFold(values []string, want string) bool {
	for _, v := range values {
		for part := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), want) {
				return true
			}
		}
	}
	return false
}
