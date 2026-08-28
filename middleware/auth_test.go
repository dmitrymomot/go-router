package middleware_test

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func keyAuthRouter(cfg middleware.KeyAuthConfig[*appContext]) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.KeyAuthWithConfig(cfg))
	r.GET("/", func(c *appContext) error { return c.String(http.StatusOK, c.Tenant) })
	return r
}

func keyValidator(c *appContext, key string) (bool, error) {
	if !middleware.SecureCompare(key, "good") {
		return false, nil
	}
	c.Tenant = "widgets"
	return true, nil
}

func bearer(key string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderAuthorization, "Bearer "+key)
	return req
}

func basicHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func TestSecureCompare(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"the same value", "s3cret", "s3cret", true},
		{"the last byte differs", "s3cret", "s3creT", false},
		{"the first byte differs", "s3cret", "S3cret", false},
		{"a prefix", "s3cret", "s3cre", false},
		{"a longer value", "s3cret", "s3crets", false},
		{"both empty", "", "", true},
		{"one empty", "", "s3cret", false},
		{"bytes beyond ASCII", "pässwörd", "pässwörd", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := middleware.SecureCompare(tt.a, tt.b); got != tt.want {
				t.Errorf("SecureCompare(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestKeyAuthAcceptsABearerToken(t *testing.T) {
	r := keyAuthRouter(middleware.KeyAuthConfig[*appContext]{Validator: keyValidator})

	rec := do(r, bearer("good"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "widgets" {
		t.Errorf("tenant = %q, want the one that the validator wrote", got)
	}
}

func TestKeyAuthPlainFormReadsTheBearerToken(t *testing.T) {
	r := newRouter()
	r.Use(middleware.KeyAuth(keyValidator))
	r.GET("/", func(c *appContext) error { return c.String(http.StatusOK, c.Tenant) })

	if got := do(r, bearer("good")).Body.String(); got != "widgets" {
		t.Errorf("tenant = %q, want %q", got, "widgets")
	}
}

func TestKeyAuthRefusesAKeyItDoesNotKnow(t *testing.T) {
	r := keyAuthRouter(middleware.KeyAuthConfig[*appContext]{Validator: keyValidator})

	if rec := do(r, bearer("bad")); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestKeyAuthReportsAMissingKey(t *testing.T) {
	calls := 0
	r := keyAuthRouter(middleware.KeyAuthConfig[*appContext]{
		Validator: func(c *appContext, key string) (bool, error) {
			calls++
			return keyValidator(c, key)
		},
	})

	tests := []struct {
		name string
		auth string
	}{
		{"no header", ""},
		{"another scheme", "Basic Zm9vOmJhcg=="},
		{"the scheme alone", "Bearer "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.auth != "" {
				req.Header.Set(router.HeaderAuthorization, tt.auth)
			}
			if rec := do(r, req); rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
	}
	if calls != 0 {
		t.Errorf("the validator ran %d times, want none for a request that carries no key", calls)
	}
}

func TestKeyAuthReadsTheConfiguredSources(t *testing.T) {
	tests := []struct {
		name    string
		source  middleware.TokenSource
		request func() *http.Request
	}{
		{
			name:   "a header of its own",
			source: middleware.FromHeader("X-Api-Key", ""),
			request: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.Header.Set("X-Api-Key", "good")
				return req
			},
		},
		{
			name:    "a query parameter",
			source:  middleware.FromQuery("key"),
			request: func() *http.Request { return httptest.NewRequest(http.MethodGet, "/?key=good", nil) },
		},
		{
			name:   "a cookie",
			source: middleware.FromCookie("key"),
			request: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.AddCookie(&http.Cookie{Name: "key", Value: "good"})
				return req
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := keyAuthRouter(middleware.KeyAuthConfig[*appContext]{
				Sources:   []middleware.TokenSource{tt.source},
				Validator: keyValidator,
			})

			if got := do(r, tt.request()).Body.String(); got != "widgets" {
				t.Errorf("tenant = %q, want %q", got, "widgets")
			}
			if rec := do(r, bearer("good")); rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401 for the default source", rec.Code)
			}
		})
	}
}

func TestKeyAuthValidatorErrorReachesTheClient(t *testing.T) {
	r := keyAuthRouter(middleware.KeyAuthConfig[*appContext]{
		Validator: func(*appContext, string) (bool, error) {
			return false, router.ErrServiceUnavailable
		},
	})

	if rec := do(r, bearer("good")); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestKeyAuthValidatorErrorSurvivesALaterWrongKey(t *testing.T) {
	r := keyAuthRouter(middleware.KeyAuthConfig[*appContext]{
		Validator: func(_ *appContext, key string) (bool, error) {
			if key == "broken" {
				return false, router.ErrServiceUnavailable
			}
			return false, nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Add(router.HeaderAuthorization, "Bearer broken")
	req.Header.Add(router.HeaderAuthorization, "Bearer bad")

	if rec := do(r, req); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503, which a wrong key must not hide", rec.Code)
	}
}

func TestKeyAuthOnErrorReplacesTheFailure(t *testing.T) {
	var seen error
	r := keyAuthRouter(middleware.KeyAuthConfig[*appContext]{
		Validator: keyValidator,
		OnError: func(_ *appContext, err error) error {
			seen = err
			return router.ErrForbidden
		},
	})

	if rec := do(r, bearer("bad")); rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if seen == nil {
		t.Fatal("OnError saw no error")
	}
	if !errors.Is(seen, router.ErrUnauthorized) {
		t.Errorf("OnError saw %v, want a 401", seen)
	}
}

func TestKeyAuthOnErrorAnswersTheRequestItself(t *testing.T) {
	r := keyAuthRouter(middleware.KeyAuthConfig[*appContext]{
		Validator: keyValidator,
		OnError: func(c *appContext, _ error) error {
			//nolint:errcheck // The test reads the recorder.
			c.String(http.StatusOK, "anonymous")
			return nil
		},
	})

	rec := do(r, bearer("bad"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "anonymous" {
		t.Errorf("body = %q, want the answer of OnError and not the handler", got)
	}
}

func TestKeyAuthContinueOnIgnoredError(t *testing.T) {
	r := keyAuthRouter(middleware.KeyAuthConfig[*appContext]{
		Validator:              keyValidator,
		OnError:                func(*appContext, error) error { return nil },
		ContinueOnIgnoredError: true,
	})

	rec := do(r, bearer("bad"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "acme" {
		t.Errorf("tenant = %q, want the unauthenticated one", got)
	}
}

func TestKeyAuthBoundsTheValidatorCalls(t *testing.T) {
	calls := 0
	r := keyAuthRouter(middleware.KeyAuthConfig[*appContext]{
		Validator: func(*appContext, string) (bool, error) {
			calls++
			return false, nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for i := range 100 {
		req.Header.Add(router.HeaderAuthorization, "Bearer key-"+string(rune('a'+i%26)))
	}

	if rec := do(r, req); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if calls != middleware.MaxTokensPerRequest {
		t.Errorf("the validator ran %d times, want %d", calls, middleware.MaxTokensPerRequest)
	}
}

func TestKeyAuthSkip(t *testing.T) {
	r := keyAuthRouter(middleware.KeyAuthConfig[*appContext]{
		Skip:      skipPath("/"),
		Validator: keyValidator,
	})

	rec := get(r, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "acme" {
		t.Errorf("tenant = %q, want the unauthenticated one", got)
	}
}

func TestKeyAuthNeedsAValidator(t *testing.T) {
	mustPanicContaining(t, "KeyAuthConfig needs a Validator", func() {
		middleware.KeyAuthWithConfig(middleware.KeyAuthConfig[*appContext]{})
	})
}

func basicAuthRouter(cfg middleware.BasicAuthConfig[*appContext]) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.BasicAuthWithConfig(cfg))
	r.GET("/", func(c *appContext) error { return c.String(http.StatusOK, c.Tenant) })
	return r
}

func basicValidator(c *appContext, user, pass string) (bool, error) {
	if !middleware.SecureCompare(user, "ops") || !middleware.SecureCompare(pass, "p:ss word") {
		return false, nil
	}
	c.Tenant = user
	return true, nil
}

func TestBasicAuthAcceptsTheCredentials(t *testing.T) {
	r := basicAuthRouter(middleware.BasicAuthConfig[*appContext]{Validator: basicValidator})

	tests := []struct {
		name string
		auth string
	}{
		{"the credentials", basicHeader("ops", "p:ss word")},
		{"the scheme in lower case", "basic " + basicHeader("ops", "p:ss word")[len("Basic "):]},
		{"the scheme in upper case", "BASIC " + basicHeader("ops", "p:ss word")[len("Basic "):]},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(router.HeaderAuthorization, tt.auth)

			rec := do(r, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != "ops" {
				t.Errorf("user = %q, want %q", got, "ops")
			}
		})
	}
}

func TestBasicAuthPlainFormAcceptsTheCredentials(t *testing.T) {
	r := newRouter()
	r.Use(middleware.BasicAuth(basicValidator))
	r.GET("/", func(c *appContext) error { return c.String(http.StatusOK, c.Tenant) })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderAuthorization, basicHeader("ops", "p:ss word"))
	if got := do(r, req).Body.String(); got != "ops" {
		t.Errorf("user = %q, want %q", got, "ops")
	}
}

func TestBasicAuthChallengesAWrongOrMissingCredential(t *testing.T) {
	r := basicAuthRouter(middleware.BasicAuthConfig[*appContext]{Validator: basicValidator})

	tests := []struct {
		name string
		auth string
	}{
		{"no header", ""},
		{"the scheme alone", "Basic"},
		{"the scheme and nothing else", "Basic "},
		{"another scheme", "Bearer abc"},
		{"a wrong password", basicHeader("ops", "guess")},
		{"a user that no one knows", basicHeader("other", "p:ss word")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.auth != "" {
				req.Header.Set(router.HeaderAuthorization, tt.auth)
			}

			rec := do(r, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if got := rec.Header().Get(router.HeaderWWWAuthenticate); got != `Basic realm="Restricted"` {
				t.Errorf("challenge = %q, want the default realm", got)
			}
		})
	}
}

func TestBasicAuthMalformedCredentialsAreABadRequest(t *testing.T) {
	r := basicAuthRouter(middleware.BasicAuthConfig[*appContext]{Validator: basicValidator})

	tests := []struct {
		name string
		auth string
	}{
		{"base64 that does not decode", "Basic !!not-base64!!"},
		{"a payload without a colon", "Basic " + base64.StdEncoding.EncodeToString([]byte("ops"))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(router.HeaderAuthorization, tt.auth)

			if rec := do(r, req); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestBasicAuthChallengeQuotesTheRealm(t *testing.T) {
	tests := []struct {
		name  string
		realm string
		want  string
	}{
		{"the default realm", "", `Basic realm="Restricted"`},
		{"a realm of its own", "Admin area", `Basic realm="Admin area"`},
		{"a realm that carries a quote", `Ad"min`, `Basic realm="Ad\"min"`},
		{"a realm that carries a line break", "Ad\r\nmin", `Basic realm="Ad\r\nmin"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := basicAuthRouter(middleware.BasicAuthConfig[*appContext]{
				Validator: basicValidator,
				Realm:     tt.realm,
			})

			if got := get(r, "/").Header().Get(router.HeaderWWWAuthenticate); got != tt.want {
				t.Errorf("challenge = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBasicAuthCallsTheValidatorOnce(t *testing.T) {
	calls := 0
	r := basicAuthRouter(middleware.BasicAuthConfig[*appContext]{
		Validator: func(*appContext, string, string) (bool, error) {
			calls++
			return false, nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for range 10 {
		req.Header.Add(router.HeaderAuthorization, basicHeader("ops", "guess"))
	}

	if rec := do(r, req); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if calls != 1 {
		t.Errorf("the validator ran %d times, want 1", calls)
	}
}

func TestBasicAuthValidatorErrorReachesTheClient(t *testing.T) {
	r := basicAuthRouter(middleware.BasicAuthConfig[*appContext]{
		Validator: func(*appContext, string, string) (bool, error) {
			return false, router.ErrServiceUnavailable
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderAuthorization, basicHeader("ops", "p:ss word"))
	if rec := do(r, req); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestBasicAuthSkip(t *testing.T) {
	r := basicAuthRouter(middleware.BasicAuthConfig[*appContext]{
		Skip:      skipPath("/"),
		Validator: basicValidator,
	})

	rec := get(r, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get(router.HeaderWWWAuthenticate); got != "" {
		t.Errorf("challenge = %q, want none", got)
	}
}

func TestBasicAuthNeedsAValidator(t *testing.T) {
	mustPanicContaining(t, "BasicAuthConfig needs a Validator", func() {
		middleware.BasicAuthWithConfig(middleware.BasicAuthConfig[*appContext]{})
	})
}
