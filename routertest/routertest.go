// Package routertest holds helpers for testing a handler or a router.
//
// Every helper takes an [http.Handler], so it works with a router, a scope
// served on its own, or any standard handler:
//
//	res := routertest.Do(r, http.MethodPost, "/users", routertest.JSONBody(in))
//	res.AssertStatus(t, http.StatusCreated)
//	out, err := res.JSON[User]()
package routertest

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// RequestOption changes a request that [Request] builds.
type RequestOption func(*http.Request)

// Header sets a request header.
func Header(key, value string) RequestOption {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

// Cookie adds a cookie to the request.
func Cookie(c *http.Cookie) RequestOption {
	return func(r *http.Request) { r.AddCookie(c) }
}

// Body sets a raw request body.
func Body(contentType string, r io.Reader) RequestOption {
	return func(req *http.Request) {
		setBody(req, contentType, r)
	}
}

// JSONBody encodes v as the request body and sets the JSON media type.
func JSONBody(v any, opts ...json.Options) RequestOption {
	return func(req *http.Request) {
		data, err := json.Marshal(v, opts...)
		if err != nil {
			panic("routertest: encode JSON body: " + err.Error())
		}
		setBody(req, "application/json", bytes.NewReader(data))
	}
}

// FormBody encodes values as an HTML form body.
func FormBody(values url.Values) RequestOption {
	return func(req *http.Request) {
		setBody(req, "application/x-www-form-urlencoded", strings.NewReader(values.Encode()))
	}
}

func setBody(req *http.Request, contentType string, r io.Reader) {
	rc, ok := r.(io.ReadCloser)
	if !ok {
		rc = io.NopCloser(r)
	}
	req.Body = rc
	req.Header.Set("Content-Type", contentType)
	if l, ok := r.(interface{ Len() int }); ok {
		req.ContentLength = int64(l.Len())
	}
}

// Request builds a request for a handler under test.
func Request(method, target string, opts ...RequestOption) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	for _, opt := range opts {
		opt(req)
	}
	return req
}

// Response is the answer of a handler, with the body already read.
type Response struct {
	*http.Response

	// Body is the whole response body.
	Body []byte

	// Recorder is the recorder that captured the answer.
	Recorder *httptest.ResponseRecorder
}

// Serve sends a request to a handler and returns the answer.
func Serve(h http.Handler, req *http.Request) *Response {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(body))
	return &Response{Response: res, Body: body, Recorder: rec}
}

// Do builds a request and sends it to a handler.
func Do(h http.Handler, method, target string, opts ...RequestOption) *Response {
	return Serve(h, Request(method, target, opts...))
}

// Get sends a GET request to a handler.
func Get(h http.Handler, target string, opts ...RequestOption) *Response {
	return Do(h, http.MethodGet, target, opts...)
}

// String returns the response body as text.
func (r *Response) String() string { return string(r.Body) }

// JSON decodes the response body into a value of type T.
func (r *Response) JSON[T any](opts ...json.Options) (T, error) {
	var v T
	err := json.Unmarshal(r.Body, &v, opts...)
	return v, err
}

// AssertStatus fails the test when the status differs from want. It prints the
// body, which usually names the reason.
func (r *Response) AssertStatus(tb testing.TB, want int) {
	tb.Helper()
	if r.StatusCode != want {
		tb.Fatalf("status = %d, want %d; body: %s", r.StatusCode, want, r.Body)
	}
}

// AssertBody fails the test when the body differs from want.
func (r *Response) AssertBody(tb testing.TB, want string) {
	tb.Helper()
	if got := r.String(); got != want {
		tb.Fatalf("body = %q, want %q", got, want)
	}
}

// AssertHeader fails the test when a response header differs from want.
func (r *Response) AssertHeader(tb testing.TB, key, want string) {
	tb.Helper()
	if got := r.Header.Get(key); got != want {
		tb.Fatalf("header %s = %q, want %q", key, got, want)
	}
}

// NewServer starts a real HTTP server for a handler and stops it when the test
// ends. Use it for a test that needs a live client, such as one that follows a
// redirect or reads a streaming body.
func NewServer(tb testing.TB, h http.Handler) *httptest.Server {
	return httptest.NewTestServer(tb, h)
}
