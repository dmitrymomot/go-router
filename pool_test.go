package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
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

func TestPoolForgetsTheBodyLimitOfAnEarlierRequest(t *testing.T) {
	r := NewPooled(func() *pctx { return new(pctx) }, resetPctx)
	r.MaxBodyBytes(16)
	bind := func(c *pctx) error {
		if _, err := c.Bind[map[string]string](); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
	r.POST("/raise", func(c *pctx) error {
		c.SetBodyLimit(1 << 10)
		return bind(c)
	})
	r.POST("/plain", bind)

	body := `{"k":"` + strings.Repeat("x", 100) + `"}`
	for _, step := range []struct {
		target string
		want   int
	}{
		{"/raise", http.StatusNoContent},
		{"/plain", http.StatusRequestEntityTooLarge},
		{"/raise", http.StatusNoContent},
		{"/plain", http.StatusRequestEntityTooLarge},
	} {
		rec := doBody(r, http.MethodPost, step.target, MIMEApplicationJSON, body)
		if rec.Code != step.want {
			t.Errorf("POST %s = %d, want %d", step.target, rec.Code, step.want)
		}
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
	var (
		seen      *pctx
		seenRoute *routeRecord
	)
	r := newPooledRouter()
	r.ErrorHandler(func(c *pctx, err error) error {
		seen, seenRoute = c, c.route
		return c.NoContent(StatusOf(err))
	})
	r.GET("/backtrack/{value}/wanted", func(c *pctx) error { return c.NoContent(http.StatusNoContent) })
	r.GET("/method/{value}", func(c *pctx) error { return c.NoContent(http.StatusNoContent) })
	r.Meta("m").Host("{tenant}.example.com", func(h *Router[*pctx]) {
		h.GET("/{a}/{b}/{c}/{d}/{e}/{f}/{g}/{h}/{tail...}", func(c *pctx) error {
			seen, seenRoute = c, c.route
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
		// req points at the released-request sentinel rather than nil, so that a
		// Base held past its handler answers as a finished context instead of
		// dereferencing nil. It must not point at the request that just ended.
		if b.req != releasedRequest || b.res != nil || b.queryCache != nil || b.deferred != nil {
			t.Fatal("pooled context retained completed request state")
		}
		if len(b.store) != 0 || b.resStorage.ResponseWriter != nil || b.resStorage.before != nil {
			t.Fatal("pooled context retained request-owned references")
		}
		if b.host != "" || b.rawTail != "" || cap(b.paramVals) > len(b.paramArr) {
			t.Fatal("pooled context retained matched request data")
		}
		if b.errorHandled {
			t.Fatal("pooled context retained the handled-error flag")
		}
		// The route record belongs to the router, not to the request, and init
		// resets it before the next request reads it.
		if b.route != nil && b.route != seenRoute {
			t.Fatal("pooled context points at a route record the request did not match")
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

// A flag left up by HandleError would make the next request on the context
// skip its error handler and answer an empty 200.
func TestPoolClearsTheHandledErrorFlag(t *testing.T) {
	captureLogs(t)

	calls := 0
	r := newPooledRouter()
	r.ErrorHandler(func(c *pctx, err error) error {
		calls++
		return c.String(StatusOf(err), "handled")
	})
	r.Use(func(next HandlerFunc[*pctx]) HandlerFunc[*pctx] {
		return func(c *pctx) error {
			err := next(c)
			if c.RoutePattern() == "/early" {
				HandleError(c, err)
			}
			return err
		}
	})
	r.GET("/early", func(*pctx) error { return ErrConflict })
	r.GET("/late", func(*pctx) error { return ErrConflict })

	for range 5 {
		do(r, http.MethodGet, "/early")
		calls = 0
		rec := do(r, http.MethodGet, "/late")
		if rec.Code != http.StatusConflict || rec.Body.String() != "handled" || calls != 1 {
			t.Fatalf("after HandleError, the next request answered %d %q with %d handler calls, want 409 %q with 1",
				rec.Code, rec.Body.String(), calls, "handled")
		}
	}
}

// betteralign keeps the fields in order but does not stop Base from growing.
// Base and a string of the application share a 320-byte size class.
func TestBaseStaysInItsSizeClass(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("the size classes are those of a 64-bit platform")
	}
	if got := unsafe.Sizeof(Base{}); got != 296 {
		t.Errorf("unsafe.Sizeof(Base{}) = %d, want 296", got)
	}
	if got := unsafe.Sizeof(tctx{}); got > 320 {
		t.Errorf("a Base plus a string is %d bytes, want at most 320", got)
	}
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

func BenchmarkMetaRoute(b *testing.B) {
	type permission string
	r := NewPooled(func() *pctx { return new(pctx) }, resetPctx)
	r.Meta(permission("admin")).GET("/users/{id}", func(c *pctx) error {
		if p, ok := MetaAs[permission](c); !ok || p != "admin" {
			return ErrForbidden
		}
		return c.NoContent(http.StatusOK)
	})
	benchServe(b, r, &nopWriter{h: make(http.Header)}, "/users/42")
}

// A pooled context must not carry a parameter from the request before it: not
// into the next response, and not into the pool, where the string would stay
// reachable for as long as the context sits there. The wide route in each case
// is deeper than the narrow one, so the values the trie writes on the way down
// sit above the slots the narrow route owns.
func TestPoolDoesNotCarryParametersBetweenRequests(t *testing.T) {
	cases := []struct {
		name, wide, narrow     string
		wideTarget, narrowPath string
		narrowBody             string
	}{
		{
			name:       "inline",
			wide:       "/p/{one}/deep/{two}/{three}",
			narrow:     "/p/{one}/other",
			wideTarget: "/p/wide1/deep/wide2/wide3",
			narrowPath: "/p/narrow1/other",
			narrowBody: "one=narrow1",
		},
		{
			// Six parameters against an InlineParamBudget of four: paramVals
			// spills to the heap, and what stays in paramArr is whatever the
			// trie wrote before the spill.
			name:       "spilled",
			wide:       "/q/{a}/{b}/{c}/{d}/{e}/{f}",
			narrow:     "/q/{a}/end",
			wideTarget: "/q/wide1/wide2/wide3/wide4/wide5/wide6",
			narrowPath: "/q/narrowA/end",
			narrowBody: "a=narrowA",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen *pctx
			r := newPooledRouter()
			r.GET(tc.wide, func(c *pctx) error { return c.NoContent(http.StatusNoContent) })
			r.GET(tc.narrow, func(c *pctx) error {
				seen = c
				names := c.ParamNames()
				parts := make([]string, len(names))
				for i, n := range names {
					parts[i] = n + "=" + c.Param(n)
				}
				return c.String(http.StatusOK, strings.Join(parts, ","))
			})

			// Three rounds: the first fills the pool, the rest run on a context
			// the wide request already used.
			for round := range 3 {
				do(r, http.MethodGet, tc.wideTarget)
				seen = nil
				got := do(r, http.MethodGet, tc.narrowPath).Body.String()
				if got != tc.narrowBody {
					t.Fatalf("round %d: body = %q, want %q", round, got, tc.narrowBody)
				}
				if seen == nil {
					t.Fatal("the narrow handler did not run")
				}
				for i, value := range seen.paramArr {
					if value != "" {
						t.Fatalf("round %d: paramArr[%d] = %q after the pool took the context back", round, i, value)
					}
				}
			}
		})
	}
}

// The trailing-slash redirect answers from inside route, before any handler
// runs, and it gets there by running the trie once to see whether the trimmed
// path matches. That probe matched, so the trie never unwound its scratch: the
// values are still in paramArr when the context goes back to the pool.
func TestPoolDoesNotCarryParametersFromATrailingSlashRedirect(t *testing.T) {
	var seen *pctx
	r := newPooledRouter()
	r.RedirectTrailingSlash(true)
	r.Pre(func(next HandlerFunc[*pctx]) HandlerFunc[*pctx] {
		return func(c *pctx) error {
			seen = c
			return next(c)
		}
	})
	r.GET("/p/{one}/deep/{two}/{three}", func(c *pctx) error { return c.NoContent(http.StatusNoContent) })

	for round := range 3 {
		seen = nil
		rec := do(r, http.MethodGet, "/p/wide1/deep/wide2/wide3/")
		if rec.Code != http.StatusMovedPermanently {
			t.Fatalf("round %d: status = %d, want %d", round, rec.Code, http.StatusMovedPermanently)
		}
		if seen == nil {
			t.Fatal("the pre-routing middleware did not run")
		}
		for i, value := range seen.paramArr {
			if value != "" {
				t.Fatalf("round %d: paramArr[%d] = %q after the pool took the context back", round, i, value)
			}
		}
	}
}

// A Base kept past its handler must read as a finished context, not dereference
// a nil request.
func TestBaseHeldPastItsRequestReadsAsCancelled(t *testing.T) {
	r := newPooledRouter()
	held := make(chan *pctx, 1)
	r.GET("/a", func(c *pctx) error {
		held <- c
		return c.NoContent(http.StatusNoContent)
	})
	do(r, http.MethodGet, "/a")

	c := <-held
	select {
	case <-c.Done():
	default:
		t.Error("Done() on a released context did not report the request as over")
	}
	if !errors.Is(c.Err(), context.Canceled) {
		t.Errorf("Err() = %v, want context.Canceled", c.Err())
	}
	if _, ok := c.Deadline(); ok {
		t.Error("Deadline() on a released context reported one")
	}
	if v := c.Value("anything"); v != nil {
		t.Errorf("Value() = %v, want nil", v)
	}
}

// Path, URL and Header reach through the request, so the released sentinel has
// to carry both or it dereferences nil where it is meant to stop.
func TestReleasedBaseReadsItsRequestFields(t *testing.T) {
	r := newPooledRouter()
	held := make(chan *pctx, 1)
	r.GET("/a", func(c *pctx) error {
		held <- c
		return c.NoContent(http.StatusNoContent)
	})
	do(r, http.MethodGet, "/a")

	c := <-held
	if got := c.Path(); got != "" {
		t.Errorf("Path() on a released context = %q, want empty", got)
	}
	if c.URL() == nil {
		t.Error("URL() on a released context is nil")
	}
	if c.Header() == nil {
		t.Error("Header() on a released context is nil")
	}
}

func TestPoolDoesNotCarryRouteMetaBetweenRequests(t *testing.T) {
	for _, observed := range []bool{false, true} {
		t.Run(fmt.Sprint("observed=", observed), func(t *testing.T) {
			var metas [][]any
			r := newPooledRouter()
			if observed {
				r.Observe(func(Context, int, int64, time.Duration, error) {})
			}
			r.Use(func(next HandlerFunc[*pctx]) HandlerFunc[*pctx] {
				return func(c *pctx) error {
					metas = append(metas, c.RouteMeta())
					return next(c)
				}
			})
			r.Meta("secret").GET("/a", func(c *pctx) error { return c.NoContent(http.StatusNoContent) })
			r.GET("/b", func(c *pctx) error { return c.NoContent(http.StatusNoContent) })

			for range 3 {
				metas = nil
				do(r, http.MethodGet, "/a")
				do(r, http.MethodGet, "/missing")
				do(r, http.MethodGet, "/b")
				if len(metas) != 3 || metas[0] == nil || metas[1] != nil || metas[2] != nil {
					t.Fatalf("RouteMeta across requests = %v, want [[secret] [] []]", metas)
				}
			}
		})
	}
}
