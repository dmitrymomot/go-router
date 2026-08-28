package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type pctx struct {
	Base
	User string
	Hits int
}

func resetPctx(c *pctx) { c.User, c.Hits = "", 0 }

func newPooledRouter() *Router[*pctx] {
	return NewPooled(func() *pctx { return new(pctx) }, resetPctx)
}

func TestPoolReusesContexts(t *testing.T) {
	var seen []*pctx

	r := newPooledRouter()
	r.GET("/", func(c *pctx) error {
		seen = append(seen, c)
		return c.NoContent(http.StatusNoContent)
	})

	for range 10 {
		do(r, http.MethodGet, "/")
	}

	unique := make(map[*pctx]bool, len(seen))
	for _, c := range seen {
		unique[c] = true
	}
	if len(unique) == len(seen) {
		t.Errorf("%d requests used %d contexts; none was reused", len(seen), len(unique))
	}
}

func TestPoolResetsApplicationFields(t *testing.T) {
	r := newPooledRouter()
	r.GET("/set", func(c *pctx) error {
		c.User, c.Hits = "ann", 3
		return c.NoContent(http.StatusNoContent)
	})
	r.GET("/read", func(c *pctx) error {
		return c.Stringf(http.StatusOK, "user=%q hits=%d", c.User, c.Hits)
	})

	do(r, http.MethodGet, "/set")
	if got, want := do(r, http.MethodGet, "/read").Body.String(), `user="" hits=0`; got != want {
		t.Errorf("body = %q, want %q; the reset function did not clear the fields", got, want)
	}
}

func TestPoolResetsTheRequestState(t *testing.T) {
	r := newPooledRouter()
	r.GET("/users/{id}", func(c *pctx) error {
		c.Set("tenant", "acme")
		return c.NoContent(http.StatusNoContent)
	})
	r.GET("/plain", func(c *pctx) error {
		tenant, ok := c.Get("tenant")
		return c.Stringf(http.StatusOK, "id=%q pattern=%q tenant=%v/%v status=%d",
			c.Param("id"), c.RoutePattern(), tenant, ok, c.Response().Status)
	})

	do(r, http.MethodGet, "/users/7")
	want := `id="" pattern="/plain" tenant=<nil>/false status=0`
	if got := do(r, http.MethodGet, "/plain").Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestPoolResetsTheCachedRequestState(t *testing.T) {
	r := NewPooled(func() *pctx { return new(pctx) }, resetPctx)
	r.MaxBodyBytes(16)
	r.Host("example.com", func(h *Router[*pctx]) {
		h.POST("/first", func(c *pctx) error {
			c.Query("q")
			c.Host()
			if _, err := c.FormValues(); err == nil {
				return ErrInternalServerError.WithMessage("the oversized body parsed")
			}
			return c.NoContent(http.StatusNoContent)
		})
	})
	r.POST("/second", func(c *pctx) error {
		vals, err := c.FormValues()
		if err != nil {
			return err
		}
		return c.Stringf(http.StatusOK, "q=%q host=%q routeHost=%q name=%q",
			c.Query("q"), c.Host(), c.RouteHost(), vals.Get("name"))
	})

	first := httptest.NewRequest(http.MethodPost, "/first?q=one",
		strings.NewReader("name="+strings.Repeat("x", 100)))
	first.Host = "example.com"
	first.Header.Set(HeaderContentType, MIMEApplicationForm)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, first)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("first request: status = %d, want 204: %s", rec.Code, rec.Body)
	}

	second := httptest.NewRequest(http.MethodPost, "/second", strings.NewReader("name=ann"))
	second.Host = "other.test"
	second.Header.Set(HeaderContentType, MIMEApplicationForm)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, second)

	want := `q="" host="other.test" routeHost="" name="ann"`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestPoolDropsAContextThatPanicked(t *testing.T) {
	const rounds = 20
	built := 0

	r := NewPooled(func() *pctx { built++; return new(pctx) }, resetPctx)
	r.GET("/panic", func(c *pctx) error { panic("boom") })
	r.GET("/ok", func(c *pctx) error { return c.NoContent(http.StatusNoContent) })

	for range rounds {
		if rec := do(r, http.MethodGet, "/panic"); rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	}
	if built < rounds {
		t.Errorf("built %d contexts for %d panicking requests; a panicked context went back into the pool",
			built, rounds)
	}

	built = 0
	for range rounds {
		do(r, http.MethodGet, "/ok")
	}
	if built == rounds {
		t.Errorf("built %d contexts for %d clean requests; none was reused", built, rounds)
	}
}

func TestPoolUnderConcurrentRequests(t *testing.T) {
	r := newPooledRouter()
	r.GET("/users/{id}", func(c *pctx) error {
		c.User = c.Param("id")
		if c.User != c.Param("id") {
			return ErrInternalServerError.WithMessage("context crossed requests")
		}
		return c.String(http.StatusOK, c.User)
	})

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			id := strings.Repeat("x", i%7+1)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/"+id, nil))
			if rec.Body.String() != id {
				t.Errorf("body = %q, want %q", rec.Body.String(), id)
			}
		})
	}
	wg.Wait()
}

func TestPoolDropsCompletedRequestReferencesBeforePut(t *testing.T) {
	var seen *pctx
	r := newPooledRouter()
	r.NotFound(func(c *pctx) error {
		seen = c
		return c.NoContent(http.StatusNotFound)
	})
	r.MethodNotAllowed(func(c *pctx) error {
		seen = c
		return c.NoContent(http.StatusMethodNotAllowed)
	})
	r.GET("/backtrack/{value}/wanted", func(c *pctx) error { return c.NoContent(http.StatusNoContent) })
	r.GET("/method/{value}", func(c *pctx) error { return c.NoContent(http.StatusNoContent) })
	r.Host("{tenant}.example.com", func(h *Router[*pctx]) {
		h.GET("/{a}/{b}/{c}/{d}/{e}/{f}/{g}/{h}/{tail...}", func(c *pctx) error {
			seen = c
			req := c.Request()
			c.Set("request", req)
			c.Query("q")
			c.setHXError(ErrBadRequest)
			c.Response().Before(func() { _ = req.Method })
			return c.NoContent(http.StatusNoContent)
		})
	})

	assertCleared := func() {
		t.Helper()
		if seen == nil {
			t.Fatal("handler did not run")
		}
		b := &seen.Base
		if b.req != nil || b.res != nil || b.queryCache != nil || b.deferred != nil {
			t.Fatal("pooled context retained completed request state")
		}
		if len(b.store) != 0 || b.resStorage.ResponseWriter != nil || b.resStorage.before != nil {
			t.Fatal("pooled context retained request-owned references")
		}
		if b.host != "" || b.rawTail != "" || cap(b.paramVals) > len(b.paramArr) {
			t.Fatal("pooled context retained matched request data")
		}
		for i, value := range b.paramArr {
			if value != "" {
				t.Errorf("paramArr[%d] retained %q", i, value)
			}
		}
		for i, value := range b.paramVals {
			if value != "" {
				t.Errorf("paramVals[%d] retained %q", i, value)
			}
		}
	}

	doHost(r, http.MethodGet, "acme.example.com", "/1/2/3/4/5/6/7/8/rest/more?q=go")
	assertCleared()
	seen = nil
	do(r, http.MethodGet, "/backtrack/value/missing")
	assertCleared()
	seen = nil
	do(r, http.MethodPost, "/method/value")
	assertCleared()
}

func TestNewPooledPanicsWithoutAResetFunction(t *testing.T) {
	defer func() {
		if msg := recover(); msg == nil || !strings.Contains(msg.(string), "reset") {
			t.Errorf("panic = %v, want one that names the reset function", msg)
		}
	}()
	NewPooled(func() *pctx { return new(pctx) }, nil)
}

func BenchmarkPooledOneParam(b *testing.B) {
	r := NewPooled(func() *pctx { return new(pctx) }, resetPctx)
	r.GET("/users/{id}", func(c *pctx) error { return c.NoContent(http.StatusOK) })
	benchServe(b, r, &nopWriter{h: make(http.Header)}, "/users/42")
}

func BenchmarkPooledStatic(b *testing.B) {
	r := NewPooled(func() *pctx { return new(pctx) }, resetPctx)
	r.GET("/users/settings", func(c *pctx) error { return c.NoContent(http.StatusOK) })
	benchServe(b, r, &nopWriter{h: make(http.Header)}, "/users/settings")
}
