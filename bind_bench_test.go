package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type benchUser struct {
	Name  string   `json:"name"`
	Age   int      `json:"age"`
	Admin bool     `json:"admin"`
	Tags  []string `json:"tags"`
}

type benchMixed struct {
	Org   string `param:"org"`
	Page  int    `query:"page"`
	Trace string `header:"X-Trace"`
	Name  string `json:"name"`
	Age   int    `json:"age"`
}

type benchQuery struct {
	Page  int      `query:"page"`
	Limit int      `query:"limit"`
	Sort  string   `query:"sort"`
	IDs   []int    `query:"id"`
	Tags  []string `query:"tag"`
}

// benchBody serves req again and again, with the same body each time.
func benchBody(b *testing.B, r http.Handler, req *http.Request, body string) {
	w := &nopWriter{h: make(http.Header)}
	var sr strings.Reader
	serve := func() {
		sr.Reset(body)
		req.Body = io.NopCloser(&sr)
		req.ContentLength = int64(len(body))
		req.PostForm = nil
		r.ServeHTTP(w, req)
	}
	serve()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		serve()
	}
}

func benchBindRouter[T any](pattern string, bind func(*tctx) (T, error)) *Router[*tctx] {
	r := New(func(http.ResponseWriter, *http.Request) *tctx { return new(tctx) })
	h := func(c *tctx) error {
		if _, err := bind(c); err != nil {
			panic(err)
		}
		return c.NoContent(http.StatusOK)
	}
	r.GET(pattern, h)
	r.POST(pattern, h)
	return r
}

const benchUserJSON = `{"name":"ann","age":30,"admin":true,"tags":["a","b"]}`

func BenchmarkBindJSONMethod(b *testing.B) {
	r := benchBindRouter("/users", func(c *tctx) (benchUser, error) { return c.BindJSON[benchUser]() })
	req := httptest.NewRequest(http.MethodPost, "/users", nil)
	req.Header.Set(HeaderContentType, MIMEApplicationJSON)
	benchBody(b, r, req, benchUserJSON)
}

func BenchmarkBindJSONBody(b *testing.B) {
	r := benchBindRouter("/users", func(c *tctx) (benchUser, error) { return c.Bind[benchUser]() })
	req := httptest.NewRequest(http.MethodPost, "/users", nil)
	req.Header.Set(HeaderContentType, MIMEApplicationJSON)
	benchBody(b, r, req, benchUserJSON)
}

func BenchmarkBindMixed(b *testing.B) {
	r := benchBindRouter("/{org}/users", func(c *tctx) (benchMixed, error) { return c.Bind[benchMixed]() })
	req := httptest.NewRequest(http.MethodPost, "/acme/users?page=3", nil)
	req.Header.Set(HeaderContentType, MIMEApplicationJSON)
	req.Header.Set("X-Trace", "t1")
	benchBody(b, r, req, `{"name":"ann","age":30}`)
}

func BenchmarkBindQueryMethod(b *testing.B) {
	r := benchBindRouter("/items", func(c *tctx) (benchQuery, error) { return c.BindQuery[benchQuery]() })
	w := &nopWriter{h: make(http.Header)}
	benchServe(b, r, w, "/items?page=2&limit=50&sort=name&id=1&id=2&id=3&tag=x&tag=y")
}

func BenchmarkBindFormMethod(b *testing.B) {
	type form struct {
		Name  string `form:"name"`
		Age   int    `form:"age"`
		Admin bool   `form:"admin"`
	}
	r := benchBindRouter("/users", func(c *tctx) (form, error) { return c.BindForm[form]() })
	req := httptest.NewRequest(http.MethodPost, "/users", nil)
	req.Header.Set(HeaderContentType, MIMEApplicationForm)
	benchBody(b, r, req, "name=ann&age=30&admin=on")
}
