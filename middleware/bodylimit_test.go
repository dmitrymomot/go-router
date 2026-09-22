package middleware_test

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

type bodyLimitPayload struct {
	Name string `json:"name"`
}

func bodyLimitRouter(cfg middleware.BodyLimitConfig, ran *bool) *router.Router[*appContext] {
	r := newRouter()
	r.Use(middleware.BodyLimitWithConfig[*appContext](cfg))
	r.POST("/read", func(c *appContext) error {
		*ran = true
		n, err := io.Copy(io.Discard, c.Request().Body)
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%d", n))
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

func bindHandler(c *appContext) error {
	in, err := c.BindJSON[map[string]string]()
	if err != nil {
		return err
	}
	return c.String(http.StatusOK, fmt.Sprintf("%d", len(in["k"])))
}

func jsonOfLength(n int) string {
	return `{"k":"` + strings.Repeat("a", n-len(`{"k":""}`)) + `"}`
}

// Before, the smaller of the router's cap and the BodyLimit won, so a route
// could never read more than MaxBodyBytes through Bind.
func TestBodyLimitRaisesTheCapOfTheRouter(t *testing.T) {
	r := newRouter()
	r.MaxBodyBytes(16)
	r.With(middleware.BodyLimit[*appContext](1<<10)).POST("/bind", bindHandler)

	for _, cl := range []int64{0, -1} {
		rec := do(r, bodyLimitPost("/bind", jsonOfLength(100), cl))
		if rec.Code != http.StatusOK || rec.Body.String() != "92" {
			t.Errorf("Content-Length %d: %d %q, want 200 %q", cl, rec.Code, rec.Body.String(), "92")
		}
	}
}

func TestBodyLimitRaisesTheCapForAnUpload(t *testing.T) {
	r := newRouter()
	r.MaxBodyBytes(64)
	upload := func(c *appContext) error {
		_, fh, err := c.FormFile("doc")
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%d", fh.Size))
	}
	r.With(middleware.BodyLimit[*appContext](4<<10)).POST("/uploads", upload)
	r.POST("/notes", upload)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("doc", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(bytes.Repeat([]byte("x"), 1<<10)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	for target, want := range map[string]int{"/uploads": http.StatusOK, "/notes": http.StatusRequestEntityTooLarge} {
		req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(buf.Bytes()))
		req.Header.Set(router.HeaderContentType, mw.FormDataContentType())
		if rec := do(r, req); rec.Code != want {
			t.Errorf("POST %s = %d %q, want %d", target, rec.Code, rec.Body.String(), want)
		}
	}
}

func TestBodyLimitStackedTheSmallerWins(t *testing.T) {
	r := newRouter()
	r.Use(middleware.BodyLimit[*appContext](16))
	r.With(middleware.BodyLimit[*appContext](1<<10)).POST("/bind", bindHandler)

	for _, cl := range []int64{0, -1} {
		rec := do(r, bodyLimitPost("/bind", jsonOfLength(100), cl))
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("Content-Length %d: status = %d, want 413", cl, rec.Code)
		}
	}
}

func TestBodyLimitZeroReplacesTheCapOfTheRouter(t *testing.T) {
	r := newRouter()
	r.MaxBodyBytes(16)
	r.Use(middleware.BodyLimitWithConfig[*appContext](middleware.BodyLimitConfig{}))
	r.POST("/bind", bindHandler)

	if rec := do(r, bodyLimitPost("/bind", jsonOfLength(100), 0)); rec.Code != http.StatusOK {
		t.Errorf("status = %d %q, want 200", rec.Code, rec.Body.String())
	}
}

func TestBodyLimitCapsWhatBindReadsBehindDecompress(t *testing.T) {
	r := newRouter()
	r.Use(middleware.BodyLimit[*appContext](64), middleware.Decompress[*appContext])
	r.POST("/bind", bindHandler)

	body := gzipped(t, jsonOfLength(1000))
	if len(body) > 64 {
		t.Fatalf("the gzip body is %d bytes, so the wire cap would refuse it", len(body))
	}
	req := bodyLimitPost("/bind", string(body), 0)
	req.Header.Set(router.HeaderContentEncoding, "gzip")
	if rec := do(r, req); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d %q, want 413", rec.Code, rec.Body.String())
	}
}

// postChunked sends a 4 KiB body of unknown length to a real server, so the
// server has to close the connection after it refuses the rest.
func postChunked(t *testing.T, h http.Handler) *http.Response {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/read",
		io.MultiReader(strings.NewReader(strings.Repeat("x", 4<<10))))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(router.HeaderAcceptEncoding, "gzip")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() }) //nolint:errcheck // The test is done with it.
	return resp
}

func readAll(c *appContext) error {
	if _, err := io.Copy(io.Discard, c.Request().Body); err != nil {
		return err
	}
	return c.NoContent(http.StatusOK)
}

func TestBodyLimitClosesTheConnection(t *testing.T) {
	r := newRouter()
	r.Use(middleware.BodyLimit[*appContext](16))
	r.POST("/read", readAll)

	resp := postChunked(t, r)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
	if !resp.Close {
		t.Error("the server kept the connection open after a 413")
	}
}

// Gzip puts its writer into the ResponseWriter of the Response, and
// MaxBytesReader does not unwrap.
func TestBodyLimitClosesTheConnectionBehindGzip(t *testing.T) {
	r := newRouter()
	r.Use(middleware.Gzip[*appContext], middleware.BodyLimit[*appContext](16))
	r.POST("/read", readAll)

	resp := postChunked(t, r)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
	if !resp.Close {
		t.Error("the server kept the connection open after a 413")
	}
}
