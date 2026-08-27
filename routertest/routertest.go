// Package routertest holds helpers for testing a handler or a router.
//
// Every helper takes an [http.Handler], so it works with a router, a scope
// served on its own, or any standard handler:
//
//	res := routertest.Do(r, http.MethodPost, "/users", routertest.JSONBody(in))
//	res.AssertStatus(t, http.StatusCreated)
//	out, err := res.JSON[User]()
//
// [Events] reads a server-sent event stream back the way a client parses it.
package routertest

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"iter"
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

// Host sets the host that the request names, which is what a host route
// matches against. Setting it through [Header] does not work, because net/http
// carries the host in a field of its own.
//
//	res := routertest.Get(r, "/", routertest.Host("api.example.com"))
func Host(host string) RequestOption {
	return func(r *http.Request) { r.Host = host }
}

// HTMX marks the request as one that htmx made, which is what
// router.Base.IsHTMX reads:
//
//	res := routertest.Get(r, "/messages", routertest.HTMX())
//
// Pass [Header] for the other htmx request headers, such as HX-Target.
//
// The name is written out rather than taken from the router, because every
// helper here works with any [http.Handler] and the package imports no router.
func HTMX() RequestOption {
	return Header("HX-Request", "true")
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

// Event is one event that a server-sent event stream carried, as a client
// parses it out of the body.
type Event struct {
	// ID is the last id that the stream named at or before this event. A
	// client carries it forward the same way, and sends it back in
	// Last-Event-ID when it reconnects.
	ID string

	// Name is the event field of the frame. It is empty when the frame named
	// none, which a client reports as the type "message".
	Name string

	// Data is the data of the event, with its lines joined by a line feed.
	Data string
}

// Events parses the response body as a server-sent event stream and returns
// the events that it carried.
//
// Usage:
//
//	res := routertest.Get(r, "/events")
//	res.AssertStatus(t, http.StatusOK)
//	routertest.AssertEvents(t, res,
//		routertest.Event{Name: "tick", Data: "one"},
//		routertest.Event{Name: "tick", Data: "two"},
//	)
//
// It reads the body the way a client does: it strips one leading byte order
// mark, it drops every comment, it drops a frame that carries no data field,
// such as the retry frame that opens a stream, and it joins the data lines of
// one event with a line feed.
//
// The recorder holds the whole body, so the handler has to return before the
// body is readable. Test a stream that never ends with [NewServer] and a
// client that reads it as it arrives.
func Events(r *Response) []Event {
	var (
		events  []Event
		data    []byte
		name    string
		id      string
		hasData bool
	)
	// A client strips one leading byte order mark before it parses, so a body
	// that opens with one still names its first field.
	body := bytes.TrimPrefix(r.Body, []byte("\ufeff"))

	for line := range eventLines(body) {
		switch {
		case line == "":
			if !hasData {
				// A frame without data dispatches nothing, and it still
				// clears the name.
				name = ""
				continue
			}
			events = append(events, Event{
				ID:   id,
				Name: name,
				Data: string(bytes.TrimSuffix(data, []byte("\n"))),
			})
			data, hasData, name = data[:0], false, ""

		case strings.HasPrefix(line, ":"):
			// A comment, which a client ignores.

		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				name = value
			case "data":
				data = append(append(data, value...), '\n')
				hasData = true
			case "id":
				// A client ignores an id that holds a NUL.
				if !strings.ContainsRune(value, 0) {
					id = value
				}
			}
		}
	}
	return events
}

// AssertEvents fails the test when the events of the stream differ from want.
func AssertEvents(tb testing.TB, r *Response, want ...Event) {
	tb.Helper()
	got := Events(r)
	if len(got) != len(want) {
		tb.Fatalf("%d events, want %d; body: %s", len(got), len(want), r.Body)
	}
	for i := range want {
		if got[i] != want[i] {
			tb.Fatalf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// eventLines returns the lines of a server-sent event stream. A line ends at a
// line feed, at a carriage return, or at the pair, and a last line that no
// break ends is not a line yet, so the sequence leaves it out.
func eventLines(b []byte) iter.Seq[string] {
	return func(yield func(string) bool) {
		for len(b) > 0 {
			i := bytes.IndexAny(b, "\r\n")
			if i < 0 {
				return
			}
			if !yield(string(b[:i])) {
				return
			}
			if b[i] == '\r' && i+1 < len(b) && b[i+1] == '\n' {
				i++
			}
			b = b[i+1:]
		}
	}
}
