package routertest_test

import (
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
)

type appContext struct {
	router.Base
	DB string
}

func newContext(http.ResponseWriter, *http.Request) *appContext {
	return &appContext{DB: "primary"}
}

type user struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

type upload struct {
	Name        string   `json:"name"`
	Filenames   []string `json:"filenames"`
	ContentType string   `json:"content_type"`
	Content     string   `json:"content"`
}

func newRouter() *router.Router[*appContext] {
	r := router.New(newContext)
	r.POST("/users", func(c *appContext) error {
		in, err := c.Bind[user]()
		if err != nil {
			return err
		}
		in.Age++
		return c.JSON(http.StatusCreated, in)
	})
	r.GET("/users/{id}", func(c *appContext) error {
		return c.String(http.StatusOK, "user "+c.Param("id"))
	})
	r.POST("/login", func(c *appContext) error {
		in, err := c.BindForm[user]()
		if err != nil {
			return err
		}
		c.SetHeader("X-Who", in.Name)
		return c.NoContent(http.StatusNoContent)
	})
	r.POST("/avatars", func(c *appContext) error {
		f, fh, err := c.FormFile("avatar")
		if err != nil {
			return err
		}
		//nolint:errcheck // The part is read only.
		defer f.Close()
		body, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, upload{
			Name:        c.FormValue("name"),
			Filenames:   []string{fh.Filename},
			ContentType: fh.Header.Get("Content-Type"),
			Content:     string(body),
		})
	})
	r.POST("/documents", func(c *appContext) error {
		fhs, err := c.FormFiles("docs")
		if err != nil {
			return err
		}
		form, err := c.MultipartForm()
		if err != nil {
			return err
		}
		out := upload{Name: strings.Join(form.Value["name"], ",")}
		for _, fh := range fhs {
			out.Filenames = append(out.Filenames, fh.Filename)
		}
		return c.JSON(http.StatusOK, out)
	})
	return r
}

func TestJSONRoundTrip(t *testing.T) {
	res := routertest.Do(newRouter(), http.MethodPost, "/users",
		routertest.JSONBody(user{Name: "ann", Age: 30}))
	res.AssertStatus(t, http.StatusCreated)

	got, err := res.JSON[user]()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != (user{Name: "ann", Age: 31}) {
		t.Errorf("got %+v", got)
	}
}

func TestGetAndBody(t *testing.T) {
	res := routertest.Get(newRouter(), "/users/7")
	res.AssertStatus(t, http.StatusOK)
	res.AssertBody(t, "user 7")
}

func TestFormBodyAndHeader(t *testing.T) {
	res := routertest.Do(newRouter(), http.MethodPost, "/login",
		routertest.FormBody(url.Values{"name": {"bo"}}))
	res.AssertStatus(t, http.StatusNoContent)
	res.AssertHeader(t, "X-Who", "bo")
}

func TestRequestOptionsSetHostCookieAndBody(t *testing.T) {
	req := routertest.Request(http.MethodPatch, "/submit",
		routertest.Host("tenant.example.com"),
		routertest.Cookie(&http.Cookie{Name: "session", Value: "abc"}),
		routertest.Body("text/plain", strings.NewReader("payload")),
	)
	if req.Host != "tenant.example.com" {
		t.Errorf("host = %q", req.Host)
	}
	cookie, err := req.Cookie("session")
	if err != nil || cookie.Value != "abc" {
		t.Fatalf("cookie = %#v, %v", cookie, err)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "payload" || req.Header.Get("Content-Type") != "text/plain" || req.ContentLength != 7 {
		t.Errorf("body = %q, content type = %q, length = %d", body, req.Header.Get("Content-Type"), req.ContentLength)
	}
}

func TestHTMXOption(t *testing.T) {
	r := newRouter()
	r.GET("/panel", router.HTMXPartial(
		func(c *appContext) error { return c.String(http.StatusOK, "fragment") },
		func(c *appContext) error { return c.String(http.StatusOK, "page") },
	))

	routertest.Get(r, "/panel", routertest.HTMX()).AssertBody(t, "fragment")
	routertest.Get(r, "/panel").AssertBody(t, "page")
}

func TestNewServer(t *testing.T) {
	srv := routertest.NewServer(t, newRouter())

	res, err := srv.Client().Get(srv.URL + "/users/9")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
}

func eventRouter() *router.Router[*appContext] {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext {
		return new(appContext)
	})
	r.GET("/events", func(c *appContext) error {
		s, err := c.SSE(http.StatusOK, router.SSERetry(2*time.Second))
		if err != nil {
			return err
		}
		if err := s.Send(router.Event{ID: "1", Name: "tick", Data: "one"}); err != nil {
			return err
		}
		if err := s.Comment("ping"); err != nil {
			return err
		}
		if err := s.Send(router.Event{Data: "two\nlines"}); err != nil {
			return err
		}
		return s.Send(router.Event{ID: "3", Name: "tick", Data: "three"})
	})
	return r
}

func TestEvents(t *testing.T) {
	res := routertest.Get(eventRouter(), "/events")
	res.AssertStatus(t, http.StatusOK)
	res.AssertHeader(t, "Content-Type", "text/event-stream")
	routertest.AssertEvents(t, res,
		routertest.Event{ID: "1", Name: "tick", Data: "one"},
		routertest.Event{ID: "1", Data: "two\nlines"},
		routertest.Event{ID: "3", Name: "tick", Data: "three"},
	)
}

func TestEventsParsing(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		want   []routertest.Event
	}{
		{"empty stream", "", nil},
		{"byte order mark", "\ufeffevent: tick\ndata: one\n\n", []routertest.Event{{Name: "tick", Data: "one"}}},
		{"one event", "data: one\n\n", []routertest.Event{{Data: "one"}}},
		{"carriage returns", "event: tick\r\ndata: one\r\n\r\n", []routertest.Event{{Name: "tick", Data: "one"}}},
		{"lone carriage returns", "event: tick\rdata: one\r\r", []routertest.Event{{Name: "tick", Data: "one"}}},
		{"no space after the colon", "data:one\n\n", []routertest.Event{{Data: "one"}}},
		{"one space only", "data:  one\n\n", []routertest.Event{{Data: " one"}}},
		{"comments", ": ping\ndata: one\n: ping\n\n", []routertest.Event{{Data: "one"}}},
		{"retry frame", "retry: 2000\n\ndata: one\n\n", []routertest.Event{{Data: "one"}}},
		{"no data field", "event: tick\n\ndata: one\n\n", []routertest.Event{{Data: "one"}}},
		{"empty data", "event: tick\ndata: \n\n", []routertest.Event{{Name: "tick"}}},
		{"unknown field", "kind: odd\ndata: one\n\n", []routertest.Event{{Data: "one"}}},
		{"field without a colon", "data\ndata: one\n\n", []routertest.Event{{Data: "\none"}}},
		{"an id carries forward", "id: 7\ndata: one\n\ndata: two\n\n", []routertest.Event{{ID: "7", Data: "one"}, {ID: "7", Data: "two"}}},
		{"an id with a NUL", "id: a\x00b\ndata: one\n\n", []routertest.Event{{Data: "one"}}},
		{"an unterminated last frame", "data: one\n\ndata: two\n", []routertest.Event{{Data: "one"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := routertest.Get(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				//nolint:errcheck // The recorder never fails.
				io.WriteString(w, tt.stream)
			}), "/events")

			got := routertest.Events(res)
			if len(got) != len(tt.want) {
				t.Fatalf("%d events, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("event %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func getUser(c *appContext) error {
	id, err := c.ParamAs[int]("id")
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, user{Name: c.DB, Age: id})
}

func TestNewContextRunsAHandlerWithoutARouter(t *testing.T) {
	c, rec := routertest.NewContext(t, newContext,
		routertest.WithPattern("/users/{id}"),
		routertest.WithParams(map[string]string{"id": "7"}))

	if err := getUser(c); err != nil {
		t.Fatalf("getUser: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var got user
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != (user{Name: "primary", Age: 7}) {
		t.Errorf("got %+v", got)
	}
}

func TestRequestHelpersRejectNilInputs(t *testing.T) {
	tests := []struct {
		name string
		call func()
	}{
		{name: "request option", call: func() { routertest.Request(http.MethodGet, "/", nil) }},
		{name: "body reader", call: func() { routertest.Body("text/plain", nil) }},
		{name: "cookie", call: func() { routertest.Cookie(nil) }},
		{name: "Serve handler", call: func() { routertest.Serve(nil, routertest.Request(http.MethodGet, "/")) }},
		{name: "Serve request", call: func() { routertest.Serve(http.NotFoundHandler(), nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("call accepted a nil input")
				}
			}()
			tt.call()
		})
	}
}

func TestContextHelpersRejectNilInputs(t *testing.T) {
	tests := []struct {
		name string
		call func(*recordingTB)
	}{
		{name: "server handler", call: func(tb *recordingTB) {
			routertest.NewServer(tb, nil)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tb := new(recordingTB)
			tt.call(tb)
			if !tb.failed || !strings.Contains(tb.msg, "needs") && !strings.Contains(tb.msg, "nil") {
				t.Fatalf("failure = %v, %q; want a clear validation failure", tb.failed, tb.msg)
			}
		})
	}
}

func TestNewContextOptions(t *testing.T) {
	tests := []struct {
		name        string
		opts        []routertest.ContextOption
		wantPattern string
		wantID      string
		wantMethod  string
		wantPath    string
	}{
		{
			name:       "no option",
			wantMethod: http.MethodGet,
			wantPath:   "/",
		},
		{
			name:        "a pattern",
			opts:        []routertest.ContextOption{routertest.WithPattern("/users/{id}")},
			wantPattern: "/users/{id}",
			wantMethod:  http.MethodGet,
			wantPath:    "/",
		},
		{
			name:       "parameters",
			opts:       []routertest.ContextOption{routertest.WithParams(map[string]string{"id": "7"})},
			wantID:     "7",
			wantMethod: http.MethodGet,
			wantPath:   "/",
		},
		{
			name: "a request",
			opts: []routertest.ContextOption{
				routertest.WithRequest(routertest.Request(http.MethodPost, "/users/7?tab=orders")),
			},
			wantMethod: http.MethodPost,
			wantPath:   "/users/7",
		},
		{
			name: "all of them",
			opts: []routertest.ContextOption{
				routertest.WithPattern("/users/{id}"),
				routertest.WithParams(map[string]string{"id": "7"}),
				routertest.WithRequest(routertest.Request(http.MethodDelete, "/users/7")),
			},
			wantPattern: "/users/{id}",
			wantID:      "7",
			wantMethod:  http.MethodDelete,
			wantPath:    "/users/7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := routertest.NewContext(t, newContext, tt.opts...)

			if got := c.RoutePattern(); got != tt.wantPattern {
				t.Errorf("pattern = %q, want %q", got, tt.wantPattern)
			}
			if got := c.Request().Pattern; got != tt.wantPattern {
				t.Errorf("request pattern = %q, want %q", got, tt.wantPattern)
			}
			if got := c.Param("id"); got != tt.wantID {
				t.Errorf("id = %q, want %q", got, tt.wantID)
			}
			if got := c.Method(); got != tt.wantMethod {
				t.Errorf("method = %q, want %q", got, tt.wantMethod)
			}
			if got := c.Path(); got != tt.wantPath {
				t.Errorf("path = %q, want %q", got, tt.wantPath)
			}
		})
	}
}

func TestNewContextNamesEveryParameter(t *testing.T) {
	c, _ := routertest.NewContext(t, newContext,
		routertest.WithParams(map[string]string{"tab": "orders", "id": "7"}))

	if got := c.ParamNames(); !slices.Equal(got, []string{"id", "tab"}) {
		t.Errorf("names = %v, want [id tab]", got)
	}
	if got := c.Param("tab"); got != "orders" {
		t.Errorf("tab = %q, want orders", got)
	}
	if _, ok := c.ParamOK("absent"); ok {
		t.Error("ParamOK reported a parameter that the options never named")
	}
}

func TestNewContextRecordsTheAnswer(t *testing.T) {
	c, rec := routertest.NewContext(t, newContext)

	if err := c.JSON(http.StatusCreated, user{Name: "ann", Age: 30}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Errorf("recorder status = %d, want 201", rec.Code)
	}
	if got := c.Response().Status; got != http.StatusCreated {
		t.Errorf("response status = %d, want 201", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("content type = %q", got)
	}
}

func TestNewContextCarriesTheRequestBody(t *testing.T) {
	c, _ := routertest.NewContext(t, newContext,
		routertest.WithRequest(routertest.Request(http.MethodPost, "/users",
			routertest.JSONBody(user{Name: "ann", Age: 30}))))

	got, err := c.Bind[user]()
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if got != (user{Name: "ann", Age: 30}) {
		t.Errorf("got %+v", got)
	}
}

type pointerContext struct {
	*router.Base
}

func TestNewContextFillsAPointerBase(t *testing.T) {
	c, _ := routertest.NewContext(t,
		func(w http.ResponseWriter, r *http.Request) *pointerContext {
			return &pointerContext{Base: router.NewBase(w, r)}
		},
		routertest.WithParams(map[string]string{"id": "7"}))

	if got := c.Param("id"); got != "7" {
		t.Errorf("id = %q, want 7", got)
	}
}

func TestNewContextReportsANilBase(t *testing.T) {
	tb := new(recordingTB)

	routertest.NewContext(tb, func(http.ResponseWriter, *http.Request) *pointerContext {
		return new(pointerContext)
	})

	if !tb.failed {
		t.Fatal("NewContext accepted a context whose router.Base is nil")
	}
	if !strings.Contains(tb.msg, "router.NewBase") {
		t.Errorf("message = %q; it has to name the fix", tb.msg)
	}
}

func TestMultipartBodyPostsAFile(t *testing.T) {
	res := routertest.Do(newRouter(), http.MethodPost, "/avatars",
		routertest.MultipartBody(
			url.Values{"name": {"ann"}},
			routertest.FilePart{
				Field:       "avatar",
				Filename:    "a.png",
				ContentType: "image/png",
				Content:     []byte("png bytes"),
			},
		))
	res.AssertStatus(t, http.StatusOK)

	got, err := res.JSON[upload]()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := upload{
		Name:        "ann",
		Filenames:   []string{"a.png"},
		ContentType: "image/png",
		Content:     "png bytes",
	}
	if got.Name != want.Name || got.ContentType != want.ContentType || got.Content != want.Content ||
		!slices.Equal(got.Filenames, want.Filenames) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestMultipartBodyDefaultsTheFilePart(t *testing.T) {
	res := routertest.Do(newRouter(), http.MethodPost, "/avatars",
		routertest.MultipartBody(nil, routertest.FilePart{Field: "avatar", Content: []byte("x")}))
	res.AssertStatus(t, http.StatusOK)

	got, err := res.JSON[upload]()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !slices.Equal(got.Filenames, []string{"avatar"}) {
		t.Errorf("filenames = %v, want the field name", got.Filenames)
	}
	if got.ContentType != "application/octet-stream" {
		t.Errorf("content type = %q, want application/octet-stream", got.ContentType)
	}
}

func TestMultipartBodyPostsMoreThanOneFile(t *testing.T) {
	res := routertest.Do(newRouter(), http.MethodPost, "/documents",
		routertest.MultipartBody(
			url.Values{"name": {"ann"}},
			routertest.FilePart{Field: "docs", Filename: "one.txt", Content: []byte("one")},
			routertest.FilePart{Field: "docs", Filename: "two.txt", Content: []byte("two")},
		))
	res.AssertStatus(t, http.StatusOK)

	got, err := res.JSON[upload]()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !slices.Equal(got.Filenames, []string{"one.txt", "two.txt"}) {
		t.Errorf("filenames = %v, want both files in order", got.Filenames)
	}
	if got.Name != "ann" {
		t.Errorf("name = %q; the parsed form carries the values too", got.Name)
	}
}

func TestMultipartBodyIsMissingWithoutAFile(t *testing.T) {
	res := routertest.Do(newRouter(), http.MethodPost, "/avatars",
		routertest.MultipartBody(url.Values{"name": {"ann"}}))
	res.AssertStatus(t, http.StatusBadRequest)
}

func TestMultipartBodyWritesTheFieldsInNameOrder(t *testing.T) {
	req := routertest.Request(http.MethodPost, "/documents",
		routertest.MultipartBody(url.Values{"c": {"3"}, "a": {"1"}, "b": {"2"}}))

	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read the body: %v", err)
	}
	a := strings.Index(string(body), `name="a"`)
	b := strings.Index(string(body), `name="b"`)
	c := strings.Index(string(body), `name="c"`)
	if a < 0 || a > b || b > c {
		t.Errorf("the fields land at %d, %d and %d, want them in name order", a, b, c)
	}
	if got := req.Header.Get("Content-Type"); !strings.HasPrefix(got, "multipart/form-data; boundary=") {
		t.Errorf("content type = %q", got)
	}
	if req.ContentLength != int64(len(body)) {
		t.Errorf("content length = %d, want %d", req.ContentLength, len(body))
	}
}

func TestMultipartBodyEscapesTheNames(t *testing.T) {
	res := routertest.Do(newRouter(), http.MethodPost, "/avatars",
		routertest.MultipartBody(nil, routertest.FilePart{
			Field:    "avatar",
			Filename: `a"b.png`,
			Content:  []byte("x"),
		}))
	res.AssertStatus(t, http.StatusOK)

	got, err := res.JSON[upload]()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !slices.Equal(got.Filenames, []string{`a"b.png`}) {
		t.Errorf("filenames = %v, want the quoted name back", got.Filenames)
	}
}

const goldenPage = "<div class=\"order\">\n  <h1>Order 7</h1>\n</div>\n"

func setUpdate(tb testing.TB, on bool) {
	tb.Helper()
	f := flag.Lookup("routertest.update")
	if f == nil {
		tb.Fatal("routertest registered no routertest.update flag")
	}
	old := f.Value.String()
	if err := f.Value.Set(strconv.FormatBool(on)); err != nil {
		tb.Fatalf("set the update flag: %v", err)
	}
	tb.Cleanup(func() {
		if err := f.Value.Set(old); err != nil {
			tb.Fatalf("restore the update flag: %v", err)
		}
	})
}

var plainUpdate = flag.Bool("update", false, "rewrite the golden files of this package")

func TestAssertGoldenReadsThePlainUpdateFlag(t *testing.T) {
	const name = "plain/page.html"
	file := filepath.Join("testdata", filepath.FromSlash(name))
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(file)) })

	setUpdate(t, false)
	old := *plainUpdate
	*plainUpdate = true
	t.Cleanup(func() { *plainUpdate = old })

	routertest.AssertGolden(t, name, []byte(goldenPage))
	if got, err := os.ReadFile(file); err != nil || string(got) != goldenPage {
		t.Fatalf("read the written file: %q, %v", got, err)
	}
}

func TestAssertGoldenAcceptsTheFile(t *testing.T) {
	setUpdate(t, false)

	routertest.AssertGolden(t, "page.html", []byte(goldenPage))
}

func TestAssertGoldenReportsADifference(t *testing.T) {
	setUpdate(t, false)

	tests := []struct {
		name string
		file string
		got  string
	}{
		{"another body", "page.html", "<div>other</div>\n"},
		{"one byte more", "page.html", goldenPage + "\n"},
		{"a file that nothing wrote", "absent.html", goldenPage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tb := new(recordingTB)
			routertest.AssertGolden(tb, tt.file, []byte(tt.got))
			if !tb.failed {
				t.Fatalf("AssertGolden accepted %s", tt.name)
			}
			if !strings.Contains(tb.msg, "-routertest.update") {
				t.Errorf("message = %q; it has to name the flag that rewrites the file", tb.msg)
			}
		})
	}
}

func TestAssertGoldenWritesTheFileWithUpdate(t *testing.T) {
	const name = "written/page.html"
	file := filepath.Join("testdata", filepath.FromSlash(name))
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(file)) })

	setUpdate(t, true)
	routertest.AssertGolden(t, name, []byte(goldenPage))

	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read the written file: %v", err)
	}
	if string(got) != goldenPage {
		t.Errorf("wrote %q", got)
	}
	setUpdate(t, false)
	routertest.AssertGolden(t, name, []byte(goldenPage))
}

func TestAssertGoldenRejectsNamesOutsideTestdata(t *testing.T) {
	setUpdate(t, true)
	outside := "outside-routertest-golden.html"
	_ = os.Remove(outside)
	t.Cleanup(func() { _ = os.Remove(outside) })

	for _, name := range []string{"", ".", "../" + outside, "nested/../../" + outside, "/tmp/outside", `..\outside`} {
		t.Run(name, func(t *testing.T) {
			tb := new(recordingTB)
			routertest.AssertGolden(tb, name, []byte("unsafe"))
			if !tb.failed || !strings.Contains(tb.msg, "invalid golden file name") {
				t.Fatalf("failure = %v, %q; want invalid-name failure", tb.failed, tb.msg)
			}
		})
	}
	if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside file exists or cannot be checked: %v", err)
	}
}

func TestAssertGoldenDoesNotFollowAnEscapingSymlink(t *testing.T) {
	setUpdate(t, true)
	outside := t.TempDir()
	link := filepath.Join("testdata", "escaping-link")
	_ = os.Remove(link)
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })

	tb := new(recordingTB)
	routertest.AssertGolden(tb, "escaping-link/outside.html", []byte("unsafe"))
	if !tb.failed {
		t.Fatal("AssertGolden followed a symlink outside testdata")
	}
	if _, err := os.Stat(filepath.Join(outside, "outside.html")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside file exists or cannot be checked: %v", err)
	}
}

type recordingTB struct {
	testing.TB
	failed bool
	msg    string
}

func (tb *recordingTB) Helper() {}

func (tb *recordingTB) Fatalf(format string, args ...any) {
	tb.failed = true
	tb.msg = fmt.Sprintf(format, args...)
}
