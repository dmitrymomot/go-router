// Package routertest holds helpers for testing a handler or a router.
//
// Every helper takes an [http.Handler], so it works with a router, a scope
// served on its own, or any standard handler:
//
//	res := routertest.Do(r, http.MethodPost, "/users", routertest.JSONBody(in))
//	res.AssertStatus(t, http.StatusCreated)
//	out, err := res.JSON[User]()
//
// [NewContext] runs one handler on its own instead, with the route already
// matched:
//
//	c, rec := routertest.NewContext(t, newContext,
//		routertest.WithPattern("/users/{id}"),
//		routertest.WithParams(map[string]string{"id": "7"}))
//
//	err := getUser(c)
//
// The context comes from the factory of the application, so the handler under
// test receives its own context type, with its database handle and its user
// where it expects them. A helper that builds a context of its own can only
// hand a handler the fields that the router package knows about.
//
// [Events] reads a server-sent event stream back the way a client parses it,
// and [AssertGolden] compares rendered output with a file under testdata.
package routertest

import (
	"bytes"
	"encoding/json/v2"
	"flag"
	"fmt"
	"io"
	"iter"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
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

// FilePart is one uploaded file that [MultipartBody] writes into a request.
type FilePart struct {
	// Field is the form field that carries the file. Two parts may name the
	// same field, which is what a picker that takes more than one file sends
	// and what [router.Base.FormFiles] reads back.
	Field string

	// Filename is the name that the part reports. It defaults to Field,
	// because a part that names no file reads back as a form value and not as
	// a file.
	Filename string

	// ContentType is the media type of the part, which reaches the handler in
	// the header of the file. It defaults to application/octet-stream, the
	// type a browser sends for one it cannot name.
	ContentType string

	// Content is the body of the part.
	Content []byte
}

// MultipartBody encodes fields and files as a multipart form body and sets the
// media type, boundary and all. It is what exercises [router.Base.FormFile],
// [router.Base.FormFiles] and [router.Base.MultipartForm]:
//
//	res := routertest.Do(r, http.MethodPost, "/avatars",
//		routertest.MultipartBody(
//			url.Values{"name": {"ann"}},
//			routertest.FilePart{Field: "avatar", Filename: "a.png", Content: png},
//		))
//
// It writes the fields in name order, so one set of values always produces the
// same body.
func MultipartBody(fields url.Values, files ...FilePart) RequestOption {
	return func(req *http.Request) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for _, name := range slices.Sorted(maps.Keys(fields)) {
			for _, value := range fields[name] {
				if err := w.WriteField(name, value); err != nil {
					panic("routertest: write the form field " + name + ": " + err.Error())
				}
			}
		}
		for _, f := range files {
			if err := writeFilePart(w, f); err != nil {
				panic("routertest: write the file part " + f.Field + ": " + err.Error())
			}
		}
		if err := w.Close(); err != nil {
			panic("routertest: close the multipart body: " + err.Error())
		}
		setBody(req, w.FormDataContentType(), bytes.NewReader(buf.Bytes()))
	}
}

// writeFilePart writes one file part. It builds the part header itself,
// because CreateFormFile writes a fixed application/octet-stream and a test
// that reads the media type back needs the one it named.
func writeFilePart(w *multipart.Writer, f FilePart) error {
	filename := f.Filename
	if filename == "" {
		filename = f.Field
	}
	contentType := f.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	h := make(textproto.MIMEHeader, 2)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`,
		quoteEscaper.Replace(f.Field), quoteEscaper.Replace(filename)))
	h.Set("Content-Type", contentType)
	p, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = p.Write(f.Content)
	return err
}

// quoteEscaper escapes what a quoted header parameter may not carry raw, the
// way mime/multipart escapes it for the parts that it writes itself.
var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

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

// contextSpec is the context that the options of [NewContext] describe.
type contextSpec struct {
	req     *http.Request
	params  map[string]string
	pattern string
}

// ContextOption changes the context that [NewContext] builds.
type ContextOption func(*contextSpec)

// WithParams seeds the route parameters, the ones that the router fills in
// from the pattern that matched:
//
//	c, _ := routertest.NewContext(t, newContext,
//		routertest.WithParams(map[string]string{"id": "7"}))
//
//	c.Param("id")             // "7"
//	c.ParamAs[int]("id")      // 7, nil
//
// A map holds no order, so [router.Base.ParamNames] answers them by name
// rather than in the order that a pattern declares them.
func WithParams(params map[string]string) ContextOption {
	return func(s *contextSpec) { s.params = params }
}

// WithPattern sets the pattern of the matched route, which
// [router.Base.RoutePattern] answers and which middleware reads to label a
// metric with the route instead of the path. It declares no parameter; name
// those with [WithParams].
func WithPattern(pattern string) ContextOption {
	return func(s *contextSpec) { s.pattern = pattern }
}

// WithRequest sets the request that the context carries. It defaults to a GET
// of "/". Build one with [Request], which takes every option above:
//
//	routertest.WithRequest(routertest.Request(http.MethodPost, "/users",
//		routertest.JSONBody(in)))
func WithRequest(req *http.Request) ContextOption {
	return func(s *contextSpec) { s.req = req }
}

// NewContext builds one context of the application, along with the recorder
// that it answers into, so that a handler runs under test without a router:
//
//	c, rec := routertest.NewContext(t, newContext,
//		routertest.WithPattern("/users/{id}"),
//		routertest.WithParams(map[string]string{"id": "7"}))
//
//	if err := getUser(c); err != nil {
//		t.Fatalf("getUser: %v", err)
//	}
//	if rec.Code != http.StatusOK {
//		t.Fatalf("status = %d", rec.Code)
//	}
//
// newCtx is the factory of the application, the one that [router.New] takes,
// so the handler under test receives its own context type with its own fields
// filled in. NewContext then binds the request state and seeds the route, the
// two things that the router does before it calls a handler, and the second of
// which a handler that reads a path parameter cannot do without.
//
// The context carries the defaults of the router for the limits that a request
// obeys, because no router configured it: [router.DefaultMaxBodyBytes], the
// multipart memory of net/http, and lenient binding.
func NewContext[C router.Context](
	tb testing.TB,
	newCtx func(http.ResponseWriter, *http.Request) C,
	opts ...ContextOption,
) (C, *httptest.ResponseRecorder) {
	tb.Helper()

	var spec contextSpec
	for _, opt := range opts {
		opt(&spec)
	}
	req := spec.req
	if req == nil {
		req = httptest.NewRequest(http.MethodGet, "/", nil)
	}
	rec := httptest.NewRecorder()
	// The factory receives the wrapper, which [router.NewBase] then keeps, so
	// that the context and the caller of the factory write to one Response.
	res := &router.Response{ResponseWriter: rec}

	c := newCtx(res, req)
	b, ok := router.FromContext(c)
	if !ok || b == nil {
		tb.Fatalf("routertest: the factory built a context whose router.Base is nil; " +
			"embed router.Base by value, or fill an embedded pointer with router.NewBase")
		return c, rec
	}
	// NewBase is the one exported way to bind a Base to a request, and the
	// factory of an application rarely does it, so the state that it builds
	// lands on the Base that the context already carries.
	*b = *router.NewBase(res, req)
	names, vals := paramSlices(spec.params)
	router.SetRouteForTest(b, spec.pattern, names, vals)
	if spec.pattern != "" {
		// The router publishes the route on the request as well, which is
		// where standard middleware reads it.
		req.Pattern = spec.pattern
	}
	return c, rec
}

// paramSlices splits the parameters into the two slices that a matched route
// carries. It orders them by name, because a map holds no order and a test
// reads the same answer on every run.
func paramSlices(params map[string]string) (names, vals []string) {
	if len(params) == 0 {
		return nil, nil
	}
	names = slices.Sorted(maps.Keys(params))
	vals = make([]string, len(names))
	for i, name := range names {
		vals[i] = params[name]
	}
	return names, vals
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

// updateFlagName is the flag that rewrites a golden file.
const updateFlagName = "update"

// updateGolden is the value of the -update flag. The package registers one
// only when nothing has registered it yet, and [AssertGolden] reads whichever
// one is there, so a binary that already carries an update flag keeps it.
//
// A flag that a test package declares itself arrives after this one, because a
// package is initialized after everything it imports. Read this flag rather
// than declaring a second one, which the flag package refuses anyway.
var updateGolden = registerUpdateFlag()

// registerUpdateFlag returns the value of the -update flag, after it registers
// the flag that no other package did.
func registerUpdateFlag() flag.Value {
	if f := flag.Lookup(updateFlagName); f != nil {
		return f.Value
	}
	flag.Bool(updateFlagName, false, "rewrite the golden files that routertest.AssertGolden reads")
	return flag.Lookup(updateFlagName).Value
}

// AssertGolden fails the test when got differs from the file testdata/name.
// Run the test with -update to write the file instead, which is how the output
// of a template becomes the golden file that later runs compare against:
//
//	var buf bytes.Buffer
//	if err := view.Order(order).Render(t.Context(), &buf); err != nil {
//		t.Fatalf("render: %v", err)
//	}
//	routertest.AssertGolden(t, "order.html", buf.Bytes())
//
//	go test ./view -update
//
// The name is a slash separated path under testdata and nothing more, because
// go test runs a test with the directory of its package as the working
// directory. Read the rewritten file before you commit it: -update accepts
// whatever the code renders, including the change that broke it.
func AssertGolden(tb testing.TB, name string, got []byte) {
	tb.Helper()

	file := filepath.Join("testdata", filepath.FromSlash(name))
	if updateGolden.String() == "true" {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			tb.Fatalf("routertest: make the golden directory: %v", err)
		}
		if err := os.WriteFile(file, got, 0o644); err != nil {
			tb.Fatalf("routertest: write %s: %v", file, err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		tb.Fatalf("routertest: read %s: %v; run the test with -update to write it", file, err)
		return
	}
	if !bytes.Equal(got, want) {
		tb.Fatalf("%s differs; run the test with -update to accept the change\ngot:\n%s\nwant:\n%s",
			file, got, want)
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
