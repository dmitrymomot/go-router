package middleware_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func TestHeartbeatAnswersAnUnregisteredPath(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Heartbeat[*appContext]("/ping"))
	r.GET("/", hello)

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := do(r, httptest.NewRequest(method, "/ping", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s /ping = %d, want 200", method, rec.Code)
		}
		if ct := rec.Header().Get(router.HeaderContentType); ct != "text/plain" {
			t.Errorf("Content-Type = %q", ct)
		}
		wantBody := "."
		if method == http.MethodHead {
			wantBody = ""
		}
		if rec.Body.String() != wantBody {
			t.Errorf("%s body = %q, want %q", method, rec.Body, wantBody)
		}
	}
	if rec := get(r, "/ping/"); rec.Code != http.StatusNotFound {
		t.Errorf("/ping/ = %d, want 404", rec.Code)
	}
	if rec := do(r, httptest.NewRequest(http.MethodPost, "/ping", nil)); rec.Code == http.StatusOK {
		t.Error("POST /ping answered 200")
	}
}

func TestHeartbeatPanicsWithoutALeadingSlash(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("did not panic")
		}
	}()
	middleware.Heartbeat[*appContext]("ping")
}

func TestStripSlashes(t *testing.T) {
	var path, raw string
	h := middleware.StripSlashes(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		path, raw = r.URL.Path, r.URL.RawPath
	}))
	tests := []struct {
		target, path, raw string
	}{
		{"/users/", "/users", ""},
		{"/users///", "/users", ""},
		{"/users", "/users", ""},
		{"/", "/", ""},
		{"//", "/", ""},
		{"/a%2Fb/", "/a/b", "/a%2Fb"},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			orig := req.URL.Path
			do(h, req)
			if path != tt.path || raw != tt.raw {
				t.Errorf("got %q %q, want %q %q", path, raw, tt.path, tt.raw)
			}
			if req.URL.Path != orig {
				t.Error("the original request was changed")
			}
		})
	}
}

func TestStripSlashesFrontsTheRouter(t *testing.T) {
	r := newRouter()
	r.GET("/users", hello)
	if rec := get(middleware.StripSlashes(r), "/users/"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestMethodOverride(t *testing.T) {
	var method string
	var field string
	h := middleware.MethodOverride(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		method = r.Method
		field = r.PostFormValue("name")
	}))
	form := func(m string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(url.Values{
			"_method": {m}, "name": {"x"},
		}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req
	}
	withHeader := func(m string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set(middleware.HeaderMethodOverride, m)
		return req
	}
	tests := []struct {
		name string
		req  *http.Request
		want string
	}{
		{"header put", withHeader("PUT"), "PUT"},
		{"header lower patch", withHeader("patch"), "PATCH"},
		{"header delete", withHeader("DELETE"), "DELETE"},
		{"header get is ignored", withHeader("GET"), "POST"},
		{"header junk is ignored", withHeader("BREW"), "POST"},
		{"form delete", form("delete"), "DELETE"},
		{"form connect is ignored", form("CONNECT"), "POST"},
		{"get is never changed", func() *http.Request {
			req := httptest.NewRequest(http.MethodGet, "/?_method=DELETE", nil)
			req.Header.Set(middleware.HeaderMethodOverride, "DELETE")
			return req
		}(), "GET"},
		{"query is not read", httptest.NewRequest(http.MethodPost, "/?_method=DELETE", nil), "POST"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method, field = "", ""
			do(h, tt.req)
			if method != tt.want {
				t.Errorf("method = %q, want %q", method, tt.want)
			}
		})
	}

	do(h, form("PUT"))
	if field != "x" {
		t.Errorf("the form was not left readable: name = %q", field)
	}
}

func TestMethodOverrideReachesTheRoute(t *testing.T) {
	r := newRouter()
	r.DELETE("/items/1", hello)
	req := httptest.NewRequest(http.MethodPost, "/items/1", nil)
	req.Header.Set(middleware.HeaderMethodOverride, "DELETE")
	if rec := do(middleware.MethodOverride(r), req); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestDeprecation(t *testing.T) {
	dep := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sun := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	r := newRouter()
	r.Use(middleware.Deprecation(middleware.DeprecationConfig[*appContext]{
		Deprecated: dep, Sunset: sun, Link: "https://example.com/migrate",
	}))
	r.GET("/", hello)
	h := get(r, "/").Header()
	if got := h.Get("Deprecation"); got != "@1767225600" {
		t.Errorf("Deprecation = %q", got)
	}
	if got := h.Get("Sunset"); got != "Fri, 01 Jan 2027 00:00:00 GMT" {
		t.Errorf("Sunset = %q", got)
	}
	if got := h.Get("Link"); got != `<https://example.com/migrate>; rel="deprecation"` {
		t.Errorf("Link = %q", got)
	}
}

func TestDeprecationOptionalHeadersAndSkip(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Deprecation(middleware.DeprecationConfig[*appContext]{
		Deprecated: time.Unix(1, 0), Skip: skipPath("/skip"),
	}))
	r.GET("/", hello)
	r.GET("/skip", hello)
	h := get(r, "/").Header()
	if h.Get("Deprecation") != "@1" || h.Get("Sunset") != "" || h.Get("Link") != "" {
		t.Errorf("headers = %v", h)
	}
	if get(r, "/skip").Header().Get("Deprecation") != "" {
		t.Error("a skipped request got the header")
	}
}

func TestDeprecationPanics(t *testing.T) {
	now := time.Now()
	tests := map[string]middleware.DeprecationConfig[*appContext]{
		"zero":           {},
		"sunset earlier": {Deprecated: now, Sunset: now.Add(-time.Hour)},
		"bad link":       {Deprecated: now, Link: "https://x/>; rel=evil"},
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("did not panic")
				}
			}()
			middleware.Deprecation(cfg)
		})
	}
}

func TestAllowContentType(t *testing.T) {
	r := newRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.Use(middleware.AllowContentType[*appContext]("application/json", "Image/*"))
	r.POST("/", hello)

	post := func(ct, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		if ct != "" {
			req.Header.Set(router.HeaderContentType, ct)
		}
		return do(r, req).Code
	}
	tests := []struct {
		name, ct, body string
		want           int
	}{
		{"allowed", "application/json", "{}", http.StatusOK},
		{"parameters and case", "Application/JSON; charset=utf-8", "{}", http.StatusOK},
		{"family", "image/png", "x", http.StatusOK},
		{"other type", "text/plain", "x", http.StatusUnsupportedMediaType},
		{"missing", "", "x", http.StatusUnsupportedMediaType},
		{"malformed", "json", "x", http.StatusUnsupportedMediaType},
		{"no body", "text/plain", "", http.StatusOK},
		{"no body no type", "", "", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := post(tt.ct, tt.body); got != tt.want {
				t.Errorf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAllowContentTypePanics(t *testing.T) {
	tests := map[string][]string{
		"empty":      nil,
		"junk":       {"nonsense"},
		"parameters": {"text/plain; charset=utf-8"},
	}
	for name, types := range tests {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("did not panic")
				}
			}()
			middleware.AllowContentType[*appContext](types...)
		})
	}
}
