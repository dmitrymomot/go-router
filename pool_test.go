package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// pctx is the application context of the pooling tests.
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

func TestPoolDropsAContextThatPanicked(t *testing.T) {
	// A context whose request panicked never goes back into the pool, so every
	// panicking request has to build a fresh one. Counting the builds is
	// stable; comparing pointers is not, because the garbage collector empties
	// a sync.Pool whenever it runs.
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

	// A clean request reuses one instead.
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
		// The value must survive the whole handler, whatever the other
		// goroutines do.
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
