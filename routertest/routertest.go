// Package routertest holds helpers for testing a handler or a router.
package routertest

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"flag"
	"io"
	"io/fs"
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

// RequestOption changes the request that [Request] builds.
type RequestOption func(*http.Request)

// Header sets a request header.
func Header(key, value string) RequestOption {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

// Host sets the host of the request, for a route scoped to one.
func Host(host string) RequestOption {
	return func(r *http.Request) { r.Host = host }
}

// HTMX marks the request as one htmx made.
func HTMX() RequestOption {
	return Header(router.HeaderHXRequest, "true")
}

// A nil cookie is refused here: http.Request.AddCookie takes it and adds
// nothing, so the request would go out short of a cookie and say nothing.
func Cookie(c *http.Cookie) RequestOption {
	if c == nil {
		panic("routertest: Cookie needs a cookie")
	}
	return func(r *http.Request) { r.AddCookie(c) }
}

// A nil reader is refused here: it would reach the handler as a body that
// panics on the first read, a long way from the call that built it.
func Body(contentType string, r io.Reader) RequestOption {
	if r == nil {
		panic("routertest: Body needs a reader")
	}
	return func(req *http.Request) {
		setBody(req, contentType, r)
	}
}

// JSONBody sends v as a JSON body. It panics if v does not encode.
func JSONBody(v any, opts ...json.Options) RequestOption {
	return func(req *http.Request) {
		data, err := json.Marshal(v, opts...)
		if err != nil {
			panic("routertest: encode JSON body: " + err.Error())
		}
		setBody(req, router.MIMEApplicationJSON, bytes.NewReader(data))
	}
}

// FormBody sends values as a URL-encoded form body.
func FormBody(values url.Values) RequestOption {
	return func(req *http.Request) {
		setBody(req, router.MIMEApplicationForm, strings.NewReader(values.Encode()))
	}
}

// FilePart is one uploaded file of a multipart body. An empty Filename takes
// the field name, and an empty ContentType takes application/octet-stream.
type FilePart struct {
	Field       string
	Filename    string
	ContentType string
	Content     []byte
}

// MultipartBody sends fields and files as a multipart form. The fields go out
// in the order of their names, so the body is the same on every run.
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
	// FileContentDisposition also percent-encodes CR and LF, which the escaper
	// here let through: a filename holding one produced a malformed part.
	h.Set("Content-Disposition", multipart.FileContentDisposition(f.Field, filename))
	h.Set("Content-Type", contentType)
	p, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = p.Write(f.Content)
	return err
}

func setBody(req *http.Request, contentType string, r io.Reader) {
	rc, ok := r.(io.ReadCloser)
	if !ok {
		rc = io.NopCloser(r)
	}
	req.Body = rc
	req.Header.Set(router.HeaderContentType, contentType)
	if l, ok := r.(interface{ Len() int }); ok {
		req.ContentLength = int64(l.Len())
	}
}

// Request builds a request for a handler under test. target is a path, and it
// may carry a query.
func Request(method, target string, opts ...RequestOption) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	for _, opt := range opts {
		opt(req)
	}
	return req
}

type contextSpec struct {
	req     *http.Request
	params  map[string]string
	pattern string
}

// ContextOption configures the context that [NewContext] builds.
type ContextOption func(*contextSpec)

// WithParams gives the context the route parameters that a router would have
// filled in.
func WithParams(params map[string]string) ContextOption {
	return func(s *contextSpec) { s.params = params }
}

// WithPattern gives the context the route pattern that [router.Base.RoutePattern]
// reports.
func WithPattern(pattern string) ContextOption {
	return func(s *contextSpec) { s.pattern = pattern }
}

// WithRequest gives the context a request of your own, from [Request] or from
// httptest. Without it the context answers a GET of "/".
func WithRequest(req *http.Request) ContextOption {
	return func(s *contextSpec) { s.req = req }
}

// NewContext builds one application context and the recorder it writes to, so
// a handler can be called on its own without a router.
//
// newCtx is the factory of the application, the same one [router.New] takes.
// It has to return a context whose [router.Base] is usable: embed Base by
// value, or fill an embedded pointer with [router.NewBase].
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
	res := &router.Response{ResponseWriter: rec}

	c := newCtx(res, req)
	b, ok := router.FromContext(c)
	if !ok || b == nil {
		tb.Fatalf("routertest: the factory built a context whose router.Base is nil; " +
			"embed router.Base by value, or fill an embedded pointer with router.NewBase")
		return c, rec
	}
	*b = *router.NewBase(res, req)
	names, vals := paramSlices(spec.params)
	router.SetRouteForTest(b, spec.pattern, names, vals)
	if spec.pattern != "" {
		req.Pattern = spec.pattern
	}
	return c, rec
}

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

// Response is what a handler answered. Body holds the whole body, already
// read, and the embedded [http.Response] can be read again from the start.
type Response struct {
	*http.Response
	Body     []byte
	Recorder *httptest.ResponseRecorder
}

// Serve sends req to h and reports the answer.
//
// Serve panics if req is nil.
func Serve(h http.Handler, req *http.Request) *Response {
	// A nil request is refused here: a handler that never reads one answers it
	// without complaint, and the test passes against a request nobody made.
	if req == nil {
		panic("routertest: Serve needs a request")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(body))
	return &Response{Response: res, Body: body, Recorder: rec}
}

// Do builds a request and sends it to h. See [Request] and [Serve].
func Do(h http.Handler, method, target string, opts ...RequestOption) *Response {
	return Serve(h, Request(method, target, opts...))
}

// Get is [Do] for a GET.
func Get(h http.Handler, target string, opts ...RequestOption) *Response {
	return Do(h, http.MethodGet, target, opts...)
}

// String reports the body as text.
func (r *Response) String() string { return string(r.Body) }

// JSON decodes the body as a T.
func (r *Response) JSON[T any](opts ...json.Options) (T, error) {
	var v T
	err := json.Unmarshal(r.Body, &v, opts...)
	return v, err
}

// AssertStatus fails the test unless the status is want. The message carries
// the body, which usually says why.
func (r *Response) AssertStatus(tb testing.TB, want int) {
	tb.Helper()
	if r.StatusCode != want {
		tb.Fatalf("status = %d, want %d; body: %s", r.StatusCode, want, r.Body)
	}
}

// AssertBody fails the test unless the body is exactly want.
func (r *Response) AssertBody(tb testing.TB, want string) {
	tb.Helper()
	if got := r.String(); got != want {
		tb.Fatalf("body = %q, want %q", got, want)
	}
}

// AssertHeader fails the test unless the response header key is want.
func (r *Response) AssertHeader(tb testing.TB, key, want string) {
	tb.Helper()
	if got := r.Header.Get(key); got != want {
		tb.Fatalf("header %s = %q, want %q", key, got, want)
	}
}

// NewServer starts a real server for h and stops it when the test ends. Use it
// where the recorder is not enough, such as a stream the test reads as it
// arrives.
//
// It fails the test if h is nil.
func NewServer(tb testing.TB, h http.Handler) *httptest.Server {
	tb.Helper()
	// A nil handler is refused here: httptest.NewServer would serve
	// http.DefaultServeMux instead, and every request would come back 404.
	if h == nil {
		tb.Fatalf("routertest: NewServer needs a handler")
		return nil
	}
	return httptest.NewTestServer(tb, h)
}

// Event is one event parsed out of a server-sent event body.
type Event struct {
	ID   string
	Name string
	Data string
}

// Events parses the body as a server-sent event stream. Comments are dropped,
// the data fields of one event are joined with a line break, and an id carries
// over to the events that follow, as the format says.
func Events(r *Response) []Event {
	var (
		events  []Event
		data    []byte
		name    string
		id      string
		hasData bool
	)
	body := bytes.TrimPrefix(r.Body, []byte("\ufeff"))

	for line := range eventLines(body) {
		switch {
		case line == "":
			if !hasData {
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
				if !strings.ContainsRune(value, 0) {
					id = value
				}
			}
		}
	}
	return events
}

// AssertEvents fails the test unless the body holds exactly the events want,
// in order.
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

const updateFlagName = "routertest.update"

const plainUpdateFlagName = "update"

var updateGolden = flag.Bool(updateFlagName, false,
	"rewrite the golden files that routertest.AssertGolden reads")

func goldenUpdate() bool {
	if *updateGolden {
		return true
	}
	f := flag.Lookup(plainUpdateFlagName)
	return f != nil && f.Value.String() == "true"
}

func closeGoldenRoot(tb testing.TB, root *os.Root) {
	tb.Helper()
	if err := root.Close(); err != nil {
		tb.Errorf("routertest: close the golden directory: %v", err)
	}
}

// AssertGolden compares got with the file testdata/name, and fails the test on
// any difference. Run the test with -routertest.update, or with an -update
// flag of your own, to write the file instead.
//
// name is a slash path inside testdata, and it cannot leave that directory.
func AssertGolden(tb testing.TB, name string, got []byte) {
	tb.Helper()

	rel, err := goldenName(name)
	if err != nil {
		tb.Fatalf("routertest: invalid golden file name %q: %v", name, err)
		return
	}
	file := filepath.Join("testdata", rel)
	if goldenUpdate() {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			tb.Fatalf("routertest: make the golden directory: %v", err)
			return
		}
		root, err := os.OpenRoot("testdata")
		if err != nil {
			tb.Fatalf("routertest: open the golden directory: %v", err)
			return
		}
		defer closeGoldenRoot(tb, root)
		if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
			tb.Fatalf("routertest: make the golden directory: %v", err)
			return
		}
		if err := root.WriteFile(rel, got, 0o644); err != nil {
			tb.Fatalf("routertest: write %s: %v", file, err)
		}
		return
	}
	root, err := os.OpenRoot("testdata")
	if err != nil {
		tb.Fatalf("routertest: open the golden directory: %v", err)
		return
	}
	defer closeGoldenRoot(tb, root)
	want, err := root.ReadFile(rel)
	if err != nil {
		tb.Fatalf("routertest: read %s: %v; run the test with -%s to write it", file, err, updateFlagName)
		return
	}
	if !bytes.Equal(got, want) {
		tb.Fatalf("%s differs; run the test with -%s to accept the change\ngot:\n%s\nwant:\n%s",
			file, updateFlagName, got, want)
	}
}

func goldenName(name string) (string, error) {
	if name == "." || !fs.ValidPath(name) || strings.ContainsRune(name, '\\') {
		return "", errors.New("name must be a slash-separated file path without parent traversal")
	}
	return filepath.FromSlash(name), nil
}

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
