// Package routertest holds helpers for testing a handler or a router.
//
// [Client] sends requests as a browser does, and keeps the cookies of every
// answer. [Response.Expect] chains the checks of one answer. [Requests] builds
// one request for each route of a router, with every parameter filled in.
package routertest

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
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
	"github.com/dmitrymomot/go-router/cookie"
	"github.com/dmitrymomot/go-router/htmx"
	"github.com/dmitrymomot/go-router/internal/routerhook"
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

// Context gives the request ctx as its context, such as the t.Context() of the
// test.
//
// Context panics if ctx is nil.
func Context(ctx context.Context) RequestOption {
	if ctx == nil {
		panic("routertest: Context needs a context")
	}
	return func(r *http.Request) { *r = *r.WithContext(ctx) }
}

// RemoteAddr sets the "host:port" the request comes from, for a handler that
// keys on the caller.
func RemoteAddr(addr string) RequestOption {
	return func(r *http.Request) { r.RemoteAddr = addr }
}

// HTMX marks the request as one htmx 4 made to swap one element: it sets
// HX-Request and HX-Request-Type: partial. Add
// Header(htmx.HeaderRequestType, "full") for a boosted link or a history
// restore.
func HTMX() RequestOption {
	return func(r *http.Request) {
		r.Header.Set(htmx.HeaderRequest, "true")
		r.Header.Set(htmx.HeaderRequestType, "partial")
	}
}

// Cookie adds c to the request.
func Cookie(c *http.Cookie) RequestOption {
	// A nil cookie is refused here: http.Request.AddCookie takes it and adds
	// nothing, so the request would go out short of a cookie and say nothing.
	if c == nil {
		panic("routertest: Cookie needs a cookie")
	}
	return func(r *http.Request) { r.AddCookie(c) }
}

// Body sends r as the body, under contentType. A reader is read once, so a
// Body shared by several requests, such as a [Client] default or an option of
// [Requests], reaches only the first.
func Body(contentType string, r io.Reader) RequestOption {
	// A nil reader is refused here: it would reach the handler as a body that
	// panics on the first read, a long way from the call that built it.
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
// may carry a query. It may also be an absolute URL: its host becomes the Host
// of the request, and https marks the request as one over TLS. The request
// carries context.Background unless an option gives it another.
func Request(method, target string, opts ...RequestOption) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	for _, opt := range opts {
		opt(req)
	}
	return req
}

type contextSpec struct {
	newReq  func(context.Context) *http.Request
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
// httptest, with the context it carries. Without it or [WithTarget] the context
// answers a GET of "/". The last of WithRequest and WithTarget wins.
func WithRequest(req *http.Request) ContextOption {
	return func(s *contextSpec) {
		s.newReq = func(context.Context) *http.Request { return req }
	}
}

// WithTarget gives the context the request that [Request] builds from method,
// target and opts. The request carries the context of the test unless opts
// give one. The last of WithRequest and WithTarget wins.
func WithTarget(method, target string, opts ...RequestOption) ContextOption {
	opts = slices.Clone(opts)
	return func(s *contextSpec) {
		s.newReq = func(ctx context.Context) *http.Request {
			return Request(method, target, slices.Concat([]RequestOption{Context(ctx)}, opts)...)
		}
	}
}

// NewContext builds one application context and the recorder it writes to, so
// a handler can be called on its own without a router.
//
// newCtx is the factory of the application, the same one [router.New] takes.
// It has to return a context whose [router.Base] is usable: embed Base by
// value, or fill an embedded pointer with [router.NewBase].
//
// Without [WithRequest] the request carries tb.Context(), which ends when the
// test does.
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
	newReq := spec.newReq
	if newReq == nil {
		newReq = func(ctx context.Context) *http.Request {
			return Request(http.MethodGet, "/", Context(ctx))
		}
	}
	req := newReq(tb.Context())
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
	routerhook.SetRoute(b, spec.pattern, names, vals)
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
// [Serve] fills the Request of the embedded http.Response with the request it
// sent, so Location resolves a relative redirect; [Recorded] leaves it nil.
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
	res := Recorded(rec)
	res.Request = req
	return res
}

// Recorded reports what rec holds as a Response, so the answer of a handler
// called through [NewContext] can be checked with [Response.Expect].
//
// Recorded panics if rec is nil.
func Recorded(rec *httptest.ResponseRecorder) *Response {
	if rec == nil {
		panic("routertest: Recorded needs a recorder")
	}
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

// ErrorBody decodes an answer that [router.JSONErrorHandler] wrote. Details
// comes back as []router.FieldError when it has that shape, and as decoded
// JSON otherwise. It reports an error when the body is not JSON or carries no
// {"error": ...} envelope.
func (r *Response) ErrorBody() (router.ErrorBody, error) {
	var env struct {
		Error struct {
			Details jsontext.Value `json:"details"`
			Message string         `json:"message"`
			Cause   string         `json:"cause"`
			Status  int            `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.Body, &env); err != nil {
		return router.ErrorBody{}, fmt.Errorf("routertest: decode the error body: %w", err)
	}
	if env.Error.Status == 0 {
		return router.ErrorBody{}, fmt.Errorf("routertest: the body %q carries no error envelope", r.Body)
	}
	body := router.ErrorBody{Status: env.Error.Status, Message: env.Error.Message, Cause: env.Error.Cause}
	details := env.Error.Details
	if len(details) == 0 || details.Kind() == 'n' {
		return body, nil
	}
	var fields []router.FieldError
	if json.Unmarshal(details, &fields, json.RejectUnknownMembers(true)) == nil {
		body.Details = fields
		return body, nil
	}
	err := json.Unmarshal(details, &body.Details)
	return body, err
}

// SignedCookie reports the value of the last cookie called name that r sets,
// verified with cc. It reports false when r sets none, clears it, or sets one
// that does not verify or has run out.
//
// SignedCookie fails the test if cc is nil.
func SignedCookie(tb testing.TB, r *Response, cc *cookie.Codec, name string) (string, bool) {
	tb.Helper()
	b := carry(tb, r, cc, "SignedCookie", name)
	if b == nil {
		return "", false
	}
	v, err := cc.Get(b, name)
	return v, err == nil
}

// Flashes reports the messages of the flash cookie that r sets, verified with
// cc, as [SignedCookie] does. It reports nil when r sets none, clears it, or
// sets one that does not verify. A handler under [NewContext] reads its own
// with [cookie.Codec.Flashes].
//
// Flashes fails the test if cc is nil.
func Flashes(tb testing.TB, r *Response, cc *cookie.Codec) []cookie.Flash {
	tb.Helper()
	b := carry(tb, r, cc, "Flashes", cookie.FlashName)
	if b == nil {
		return nil
	}
	return cc.Flashes(b)
}

// carry builds a Base whose request holds the last cookie called name that r
// sets, or reports nil when r sets none or clears it.
func carry(tb testing.TB, r *Response, cc *cookie.Codec, caller, name string) *router.Base {
	tb.Helper()
	if cc == nil {
		tb.Fatalf("routertest: %s needs a codec", caller)
		return nil
	}
	var last *http.Cookie
	for _, c := range r.Cookies() {
		if c.Name == name {
			last = c
		}
	}
	if last == nil || last.MaxAge < 0 || last.Value == "" {
		return nil
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: name, Value: last.Value})
	return router.NewBase(httptest.NewRecorder(), req)
}

// FlashCookie sends flashes in a flash cookie that cc signs, as a redirect
// would have left it, for a test of the page that shows them. Without flashes
// it sends no cookie.
//
// FlashCookie fails the test if cc is nil or if the messages do not fit in one
// cookie.
func FlashCookie(tb testing.TB, cc *cookie.Codec, flashes ...cookie.Flash) RequestOption {
	tb.Helper()
	if cc == nil {
		tb.Fatalf("routertest: FlashCookie needs a codec")
		return func(*http.Request) {}
	}
	rec := httptest.NewRecorder()
	b := router.NewBase(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, f := range flashes {
		if err := cc.AddFlash(b, f); err != nil {
			tb.Fatalf("routertest: FlashCookie: %v", err)
			return func(*http.Request) {}
		}
	}
	var value string
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookie.FlashName {
			value = c.Value
		}
	}
	return func(r *http.Request) {
		if value != "" {
			r.AddCookie(&http.Cookie{Name: cookie.FlashName, Value: value})
		}
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

const updateFlagName = "routertest.update"

const plainUpdateFlagName = "update"

var updateGolden = flag.Bool(updateFlagName, false,
	"rewrite the golden files that routertest.Expect.Golden reads")

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

// writeGolden writes body to testdata/rel, making the directories it needs
// without leaving testdata.
func writeGolden(tb testing.TB, rel string, body []byte) error {
	tb.Helper()
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot("testdata")
	if err != nil {
		return err
	}
	defer closeGoldenRoot(tb, root)
	if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return err
	}
	return root.WriteFile(rel, body, 0o644)
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
