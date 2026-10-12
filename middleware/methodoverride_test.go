package middleware_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router/middleware"
)

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

func formPost(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/items/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestMethodOverrideLeavesTheBodyLimitToTheRouter(t *testing.T) {
	r := newRouter()
	r.DELETE("/items/1", func(c *appContext) error {
		c.SetBodyLimit(64 << 10)
		if _, err := c.BindForm[struct {
			Name string `form:"name"`
		}](); err != nil {
			return err
		}
		return c.String(http.StatusOK, "ok")
	})
	h := middleware.MethodOverride(r)

	big := "_method=DELETE&name=" + strings.Repeat("x", 9<<20)
	if rec := do(h, formPost(big)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("9 MB body: status = %d, want 413", rec.Code)
	}
	if rec := do(h, formPost("_method=DELETE&name=x")); rec.Code != http.StatusOK {
		t.Fatalf("small body: status = %d, want 200", rec.Code)
	}
}

func TestMethodOverrideKeepsTheWholeBody(t *testing.T) {
	type form struct {
		Name string `form:"name"`
		Tail string `form:"tail"`
	}
	r := newRouter()
	r.PUT("/items/1", func(c *appContext) error {
		f, err := c.BindForm[form]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, f.Name+"|"+f.Tail)
	})
	h := middleware.MethodOverride(r)

	name := strings.Repeat("n", 100<<10)
	body := "_method=put&name=" + name + "&tail=end"
	rec := do(h, formPost(body))
	if rec.Code != http.StatusOK || rec.Body.String() != name+"|end" {
		t.Fatalf("status = %d, body of %d bytes; want 200 with the whole form", rec.Code, rec.Body.Len())
	}
}

func TestMethodOverrideIgnoresAFieldPastThePrefix(t *testing.T) {
	var method string
	var got []byte
	h := middleware.MethodOverride(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		method = r.Method
		got, _ = io.ReadAll(r.Body)
	}))
	body := "name=" + strings.Repeat("x", 70<<10) + "&_method=DELETE"
	do(h, formPost(body))
	if method != http.MethodPost {
		t.Errorf("method = %q, want POST", method)
	}
	if string(got) != body {
		t.Errorf("body was cut: %d bytes, want %d", len(got), len(body))
	}
}
