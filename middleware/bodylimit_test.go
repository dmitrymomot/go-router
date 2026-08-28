package middleware_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// bodyLimitPayload is what the binding route of the body limit tests decodes.
type bodyLimitPayload struct {
	Name string `json:"name"`
}

// bodyLimitRouter answers on two routes: one reads the body itself, which is
// the reader that only the middleware caps, and one binds it, which the router
// caps as well.
func bodyLimitRouter(cfg middleware.BodyLimitConfig, ran *bool) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.BodyLimitWithConfig[*appContext](cfg))
	r.POST("/read", func(c *appContext) error {
		*ran = true
		n, err := io.Copy(io.Discard, c.Request().Body)
		if err != nil {
			return err
		}
		return c.Stringf(http.StatusOK, "%d", n)
	})
	r.POST("/bind", func(c *appContext) error {
		*ran = true
		in, err := c.Bind[bodyLimitPayload]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, in.Name)
	})
	return r
}

// bodyLimitPost builds a POST whose declared length is len(body) unless the
// caller names another one.
func bodyLimitPost(target, body string, contentLength int64) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set(router.HeaderContentType, router.MIMEApplicationJSON)
	if contentLength != 0 {
		req.ContentLength = contentLength
	}
	return req
}

func TestBodyLimitRejectsALongContentLength(t *testing.T) {
	ran := false
	r := bodyLimitRouter(middleware.BodyLimitConfig{Limit: 8}, &ran)

	rec := do(r, bodyLimitPost("/read", strings.Repeat("x", 64), 0))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if ran {
		t.Error("the handler ran for a body that the declared length already refused")
	}
}

func TestBodyLimitStopsABodyThatUnderstatesItsLength(t *testing.T) {
	ran := false
	r := bodyLimitRouter(middleware.BodyLimitConfig{Limit: 8}, &ran)

	// A length of -1 is what a chunked body declares, so nothing but the
	// reader itself can stop this one.
	rec := do(r, bodyLimitPost("/read", strings.Repeat("x", 64), -1))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if !ran {
		t.Error("the handler never ran, so the reader was not what stopped the body")
	}
}

func TestBodyLimitLetsAShortBodyThrough(t *testing.T) {
	ran := false
	r := bodyLimitRouter(middleware.BodyLimitConfig{Limit: 64}, &ran)

	rec := do(r, bodyLimitPost("/read", "0123456789", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "10" {
		t.Errorf("the handler read %q bytes, want 10", rec.Body.String())
	}
}

func TestBodyLimitKeepsTheStatusThatBindReports(t *testing.T) {
	ran := false
	r := bodyLimitRouter(middleware.BodyLimitConfig{Limit: 8}, &ran)

	rec := do(r, bodyLimitPost("/bind", `{"name":"a long enough name"}`, -1))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestBodyLimitZeroUsesTheRouterDefault(t *testing.T) {
	ran := false
	r := bodyLimitRouter(middleware.BodyLimitConfig{}, &ran)

	rec := do(r, bodyLimitPost("/read", "x", router.DefaultMaxBodyBytes+1))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}

	rec = do(r, bodyLimitPost("/read", "x", 0))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestBodyLimitPlainFormTakesTheLimit(t *testing.T) {
	r := newRouter()
	r.Use(middleware.BodyLimit[*appContext](8))
	r.POST("/", func(c *appContext) error { return c.NoContent(http.StatusOK) })

	if rec := do(r, bodyLimitPost("/", strings.Repeat("x", 64), 0)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestBodyLimitSkip(t *testing.T) {
	ran := false
	r := bodyLimitRouter(middleware.BodyLimitConfig{
		Limit: 8,
		Skip:  skipPath("/read"),
	}, &ran)

	rec := do(r, bodyLimitPost("/read", strings.Repeat("x", 64), 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "64" {
		t.Errorf("the handler read %q bytes, want 64", rec.Body.String())
	}
}
