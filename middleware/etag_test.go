package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func etagRouter(cfg middleware.ETagConfig[*appContext], h router.HandlerFunc[*appContext]) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.ETagWithConfig(cfg))
	r.GET("/", h)
	return r
}

func hello(c *appContext) error { return c.String(http.StatusOK, "hello") }

func TestETagSetsATagAndAnswers304(t *testing.T) {
	r := etagRouter(middleware.ETagConfig[*appContext]{}, hello)

	first := get(r, "/")
	tag := first.Header().Get(router.HeaderETag)
	if first.Code != http.StatusOK || first.Body.String() != "hello" {
		t.Fatalf("first = %d %q", first.Code, first.Body)
	}
	if len(tag) < 3 || tag[0] != '"' || tag[len(tag)-1] != '"' || strings.HasPrefix(tag, "W/") {
		t.Fatalf("ETag = %q, want a strong quoted tag", tag)
	}
	if got := first.Header().Get(router.HeaderContentLength); got != "5" {
		t.Errorf("Content-Length = %q, want 5", got)
	}

	tests := []struct {
		name string
		inm  string
		want int
	}{
		{"exact", tag, http.StatusNotModified},
		{"weak form", "W/" + tag, http.StatusNotModified},
		{"in a list", `"other", ` + tag, http.StatusNotModified},
		{"star", "*", http.StatusNotModified},
		{"different", `"nope"`, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(router.HeaderIfNoneMatch, tt.inm)
			rec := do(r, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.want == http.StatusNotModified {
				if rec.Body.Len() != 0 {
					t.Errorf("304 body = %q, want none", rec.Body)
				}
				if rec.Header().Get(router.HeaderETag) != tag {
					t.Errorf("304 ETag = %q, want %q", rec.Header().Get(router.HeaderETag), tag)
				}
			}
		})
	}
}

func TestETagHEADPassesThrough(t *testing.T) {
	r := newRouter()
	r.Use(middleware.ETag[*appContext])
	r.GET("/", hello)
	r.HEAD("/", hello)

	got := get(r, "/")
	head := do(r, httptest.NewRequest(http.MethodHead, "/", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD = %d %q, want an empty 200", head.Code, head.Body)
	}
	if got := head.Header().Get(router.HeaderContentLength); got != "5" {
		t.Errorf("HEAD Content-Length = %q, want the handler's 5", got)
	}
	if got := head.Header().Get(router.HeaderETag); got != "" {
		t.Errorf("HEAD ETag = %q, want none", got)
	}
	if cl := got.Header().Get(router.HeaderContentLength); cl != "5" || got.Header().Get(router.HeaderETag) == "" {
		t.Errorf("GET Content-Length = %q, ETag %q", cl, got.Header().Get(router.HeaderETag))
	}
}

func TestETagMatchesATagWithACommaInIt(t *testing.T) {
	r := etagRouter(middleware.ETagConfig[*appContext]{}, func(c *appContext) error {
		c.Response().Header().Set(router.HeaderETag, `"a,b"`)
		return c.String(http.StatusOK, "hello")
	})
	for _, inm := range []string{`"a,b"`, `"x", W/"a,b"`} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(router.HeaderIfNoneMatch, inm)
		if rec := do(r, req); rec.Code != http.StatusNotModified {
			t.Errorf("If-None-Match %s: status = %d, want 304", inm, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderIfNoneMatch, `"a", "b"`)
	if rec := do(r, req); rec.Code != http.StatusOK {
		t.Errorf("split halves matched: status = %d, want 200", rec.Code)
	}
}

func TestETagLeavesOtherMethodsAlone(t *testing.T) {
	r := newRouter()
	r.Use(middleware.ETag[*appContext])
	r.POST("/", hello)
	rec := do(r, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Header().Get(router.HeaderETag) != "" {
		t.Error("POST got an ETag")
	}
}

func TestETagKeepsATagTheHandlerSet(t *testing.T) {
	r := etagRouter(middleware.ETagConfig[*appContext]{}, func(c *appContext) error {
		c.Response().Header().Set(router.HeaderETag, `"v1"`)
		return c.String(http.StatusOK, "hello")
	})
	if got := get(r, "/").Header().Get(router.HeaderETag); got != `"v1"` {
		t.Fatalf("ETag = %q, want the handler's", got)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderIfNoneMatch, `W/"v1"`)
	if rec := do(r, req); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("got %d %q, want an empty 304", rec.Code, rec.Body)
	}
}

func TestETagPassesThrough(t *testing.T) {
	tests := []struct {
		name string
		cfg  middleware.ETagConfig[*appContext]
		h    router.HandlerFunc[*appContext]
	}{
		{"no-store", middleware.ETagConfig[*appContext]{}, func(c *appContext) error {
			c.Response().Header().Set(router.HeaderCacheControl, "private, No-Store")
			return c.String(http.StatusOK, "hello")
		}},
		{"not a 200", middleware.ETagConfig[*appContext]{}, func(c *appContext) error {
			return c.String(http.StatusCreated, "hello")
		}},
		{"over the cap", middleware.ETagConfig[*appContext]{MaxBytes: 4}, hello},
		{"declared over the cap", middleware.ETagConfig[*appContext]{MaxBytes: 4}, func(c *appContext) error {
			c.Response().Header().Set(router.HeaderContentLength, "5")
			return c.String(http.StatusOK, "hello")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(etagRouter(tt.cfg, tt.h), "/")
			if rec.Header().Get(router.HeaderETag) != "" {
				t.Errorf("ETag = %q, want none", rec.Header().Get(router.HeaderETag))
			}
			if rec.Body.String() != "hello" {
				t.Errorf("body = %q, want hello", rec.Body)
			}
		})
	}
}

func TestETagGrowingPastTheCapKeepsEveryByte(t *testing.T) {
	r := etagRouter(middleware.ETagConfig[*appContext]{MaxBytes: 8}, func(c *appContext) error {
		c.Response().Header().Set(router.HeaderContentType, "text/plain")
		c.Response().WriteHeader(http.StatusOK)
		for range 3 {
			if _, err := c.Response().Write([]byte("abcd")); err != nil {
				return err
			}
		}
		return nil
	})
	rec := get(r, "/")
	if rec.Body.String() != "abcdabcdabcd" || rec.Header().Get(router.HeaderETag) != "" {
		t.Fatalf("body %q, ETag %q", rec.Body, rec.Header().Get(router.HeaderETag))
	}
}

func TestETagFlushCommitsAndStopsBuffering(t *testing.T) {
	r := etagRouter(middleware.ETagConfig[*appContext]{}, func(c *appContext) error {
		_, _ = c.Response().Write([]byte("one"))
		if err := http.NewResponseController(c.Response()).Flush(); err != nil {
			t.Errorf("Flush: %v", err)
		}
		_, _ = c.Response().Write([]byte("two"))
		return nil
	})
	rec := get(r, "/")
	if !rec.Flushed || rec.Body.String() != "onetwo" || rec.Header().Get(router.HeaderETag) != "" {
		t.Fatalf("flushed %v, body %q, ETag %q", rec.Flushed, rec.Body, rec.Header().Get(router.HeaderETag))
	}
}

func TestETagKeepsUnwrap(t *testing.T) {
	var reached bool
	r := etagRouter(middleware.ETagConfig[*appContext]{}, func(c *appContext) error {
		// SetWriteDeadline is unsupported by the recorder, but the error must
		// say so, which proves the controller walked down to the recorder.
		err := http.NewResponseController(c.Response()).SetWriteDeadline(timeZero)
		reached = err != nil && strings.Contains(err.Error(), "not supported")
		return c.String(http.StatusOK, "hello")
	})
	get(r, "/")
	if !reached {
		t.Error("ResponseController did not reach the writer underneath")
	}
}

func TestETagSendsTheErrorOfAFailedHandler(t *testing.T) {
	r := etagRouter(middleware.ETagConfig[*appContext]{}, func(*appContext) error {
		return router.ErrBadRequest
	})
	rec := get(r, "/")
	if rec.Code != http.StatusBadRequest || rec.Header().Get(router.HeaderETag) != "" {
		t.Fatalf("got %d, ETag %q", rec.Code, rec.Header().Get(router.HeaderETag))
	}
}

func TestETagSkipAndPanics(t *testing.T) {
	r := etagRouter(middleware.ETagConfig[*appContext]{Skip: skipPath("/")}, hello)
	if get(r, "/").Header().Get(router.HeaderETag) != "" {
		t.Error("a skipped request got an ETag")
	}
	defer func() {
		if recover() == nil {
			t.Error("a negative MaxBytes did not panic")
		}
	}()
	middleware.ETagWithConfig(middleware.ETagConfig[*appContext]{MaxBytes: -1})
}
