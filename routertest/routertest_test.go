package routertest_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/cookie"
	"github.com/dmitrymomot/go-router/htmx"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/sse"
)

type appContext struct {
	router.Base
	DB string
}

func newContext(http.ResponseWriter, *http.Request) *appContext {
	return &appContext{DB: "primary"}
}

type user struct {
	Name string `json:"name" form:"name"`
	Age  int    `json:"age" form:"age"`
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
	res.Expect(t).Status(http.StatusCreated)

	got, err := res.JSON[user]()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != (user{Name: "ann", Age: 31}) {
		t.Errorf("got %+v", got)
	}
}

type signup struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (s signup) Validate() error {
	var errs []error
	if s.Name == "" {
		errs = append(errs, router.FieldError{Field: "name", Message: "is required"})
	}
	if s.Email == "" {
		errs = append(errs, router.FieldError{Field: "email", Message: "is required"})
	}
	return errors.Join(errs...)
}

func jsonErrorRouter() *router.Router[*appContext] {
	r := router.New(newContext)
	r.Logger(slog.New(slog.DiscardHandler))
	r.ErrorHandler(router.JSONErrorHandler[*appContext])
	r.POST("/signup", func(c *appContext) error {
		_, err := c.Bind[signup]()
		return err
	})
	r.GET("/limited", func(*appContext) error {
		return router.ErrTooManyRequests.WithDetails(map[string]int{"retry_after": 30})
	})
	r.GET("/text", func(c *appContext) error { return c.String(http.StatusOK, "plain") })
	r.GET("/other", func(c *appContext) error { return c.JSON(http.StatusOK, map[string]int{"status": 0}) })
	return r
}

func TestResponseErrorBody(t *testing.T) {
	r := jsonErrorRouter()

	t.Run("field errors", func(t *testing.T) {
		body, err := routertest.Do(r, http.MethodPost, "/signup", routertest.JSONBody(signup{})).ErrorBody()
		if err != nil {
			t.Fatal(err)
		}
		fields, ok := body.Details.([]router.FieldError)
		if body.Status != http.StatusUnprocessableEntity || !ok {
			t.Fatalf("body = %+v, want a 422 with []router.FieldError", body)
		}
		var names []string
		for _, f := range fields {
			names = append(names, f.Field)
		}
		if !slices.Equal(names, []string{"name", "email"}) {
			t.Errorf("fields = %v, want [name email]", names)
		}
	})

	t.Run("other details", func(t *testing.T) {
		body, err := routertest.Get(r, "/limited").ErrorBody()
		if err != nil {
			t.Fatal(err)
		}
		details, ok := body.Details.(map[string]any)
		if !ok || details["retry_after"] != float64(30) {
			t.Errorf("details = %#v, want map[retry_after:30]", body.Details)
		}
	})

	for _, path := range []string{"/text", "/other"} {
		t.Run("no envelope at "+path, func(t *testing.T) {
			if body, err := routertest.Get(r, path).ErrorBody(); err == nil {
				t.Errorf("ErrorBody() = %+v, want an error", body)
			}
		})
	}
}

func TestGetAndBody(t *testing.T) {
	res := routertest.Get(newRouter(), "/users/7")
	res.Expect(t).Status(http.StatusOK).Body("user 7")
}

func TestFormBodyAndHeader(t *testing.T) {
	res := routertest.Do(newRouter(), http.MethodPost, "/login",
		routertest.FormBody(url.Values{"name": {"bo"}}))
	res.Expect(t).Status(http.StatusNoContent).Header("X-Who", "bo")
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
	r.GET("/panel", htmx.Partial(
		func(c *appContext) error { return c.String(http.StatusOK, "fragment") },
		func(c *appContext) error { return c.String(http.StatusOK, "page") },
	))
	r.GET("/type", func(c *appContext) error { return c.String(http.StatusOK, htmx.RequestOf(c.Request()).RequestType) })

	routertest.Get(r, "/panel", routertest.HTMX()).Expect(t).Body("fragment")
	routertest.Get(r, "/panel").Expect(t).Body("page")
	routertest.Get(r, "/panel", routertest.HTMX(), routertest.Header(htmx.HeaderRequestType, "full")).
		Expect(t).Body("page")
	routertest.Get(r, "/type", routertest.HTMX()).Expect(t).Body("partial")
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
		s, err := sse.Open(c, http.StatusOK, sse.Retry(2*time.Second))
		if err != nil {
			return err
		}
		if err := s.Send(sse.Event{ID: "1", Name: "tick", Data: "one"}); err != nil {
			return err
		}
		if err := s.Comment("ping"); err != nil {
			return err
		}
		if err := s.Send(sse.Event{Data: "two\nlines"}); err != nil {
			return err
		}
		return s.Send(sse.Event{ID: "3", Name: "tick", Data: "three"})
	})
	return r
}

func TestEvents(t *testing.T) {
	res := routertest.Get(eventRouter(), "/events")
	res.Expect(t).Status(http.StatusOK).Header("Content-Type", "text/event-stream")
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
		{name: "recorder", call: func() { routertest.Recorded(nil) }},
		{name: "client cookie", call: func() { routertest.NewClient(t, http.NotFoundHandler()).SetCookie(nil) }},
		{name: "client follow", call: func() { routertest.NewClient(t, http.NotFoundHandler()).Follow(nil) }},
		{name: "context", call: func() { routertest.Context(nilContext) }},
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

func TestRecordedReadsTheRecorder(t *testing.T) {
	c, rec := routertest.NewContext(t, newContext)
	if err := c.Redirect(http.StatusSeeOther, "/x"); err != nil {
		t.Fatalf("Redirect: %v", err)
	}
	res := routertest.Recorded(rec)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get(router.HeaderLocation) != "/x" {
		t.Errorf("status = %d, Location = %q; want 303 to /x", res.StatusCode, res.Header.Get(router.HeaderLocation))
	}
	if res.Request != nil {
		t.Errorf("Request = %v, want nil for a recorder", res.Request)
	}
	if res.Recorder != rec {
		t.Error("Recorder is not the recorder given")
	}

	c, rec = routertest.NewContext(t, newContext)
	if err := c.JSON(http.StatusOK, user{Name: "ann", Age: 30}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	res = routertest.Recorded(rec)
	if got, err := res.JSON[user](); err != nil || got != (user{Name: "ann", Age: 30}) {
		t.Errorf("JSON = %+v, %v", got, err)
	}
	again, err := io.ReadAll(res.Response.Body)
	if err != nil || string(again) != res.String() || res.String() == "" {
		t.Errorf("body read again = %q, %v; want %q", again, err, res.String())
	}
}

func TestServeKeepsTheRequest(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(router.HeaderLocation, "next")
		w.WriteHeader(http.StatusSeeOther)
	})
	req := routertest.Request(http.MethodGet, "/a/b")
	res := routertest.Serve(h, req)
	if res.Request != req {
		t.Fatal("the response does not carry the request Serve sent")
	}
	loc, err := res.Location()
	if err != nil || loc.Path != "/a/next" {
		t.Errorf("Location = %v, %v; want /a/next", loc, err)
	}
}

// nilContext is passed where a context is refused, so staticcheck does not
// read the nil as a mistake.
var nilContext context.Context

type ctxKey struct{}

func TestContextOptionReplacesTheRequestContext(t *testing.T) {
	ctx := context.WithValue(t.Context(), ctxKey{}, "tenant-7")

	req := routertest.Request(http.MethodGet, "/", routertest.Context(ctx))
	if req.Context() != ctx {
		t.Error("the request does not carry the context of the option")
	}

	r := router.New(newContext)
	r.GET("/", func(c *appContext) error {
		v, _ := c.Value(ctxKey{}).(string)
		return c.String(http.StatusOK, v)
	})
	routertest.Get(r, "/", routertest.Context(ctx)).Expect(t).Status(http.StatusOK).Body("tenant-7")
}

func TestRequestKeepsTheBackgroundContext(t *testing.T) {
	if ctx := routertest.Request(http.MethodGet, "/").Context(); ctx != context.Background() {
		t.Errorf("Request context = %v, want context.Background", ctx)
	}
	var seen context.Context
	h := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r.Context() })
	for _, send := range []func(){
		func() { routertest.Get(h, "/") },
		func() { routertest.Do(h, http.MethodPost, "/") },
	} {
		seen = nil
		send()
		if seen != context.Background() {
			t.Errorf("handler context = %v, want context.Background", seen)
		}
	}
}

func TestRemoteAddrOption(t *testing.T) {
	req := routertest.Request(http.MethodGet, "/", routertest.RemoteAddr("203.0.113.7:4321"))
	if req.RemoteAddr != "203.0.113.7:4321" {
		t.Errorf("RemoteAddr = %q", req.RemoteAddr)
	}

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.RemoteAddr) })
	routertest.Get(h, "/", routertest.RemoteAddr("203.0.113.7:4321")).Expect(t).Body("203.0.113.7:4321")
	routertest.Get(h, "/").Expect(t).Body("192.0.2.1:1234")
}

func TestContextHelpersRejectNilInputs(t *testing.T) {
	tests := []struct {
		name string
		call func(*recordingTB)
	}{
		{name: "server handler", call: func(tb *recordingTB) {
			routertest.NewServer(tb, nil)
		}},
		{name: "client handler", call: func(tb *recordingTB) {
			routertest.NewClient(tb, nil)
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
			name: "a target",
			opts: []routertest.ContextOption{
				routertest.WithTarget(http.MethodPut, "/users/7?tab=orders"),
			},
			wantMethod: http.MethodPut,
			wantPath:   "/users/7",
		},
		{
			name: "WithRequest then WithTarget",
			opts: []routertest.ContextOption{
				routertest.WithRequest(routertest.Request(http.MethodPost, "/request")),
				routertest.WithTarget(http.MethodPut, "/target"),
			},
			wantMethod: http.MethodPut,
			wantPath:   "/target",
		},
		{
			name: "WithTarget then WithRequest",
			opts: []routertest.ContextOption{
				routertest.WithTarget(http.MethodPut, "/target"),
				routertest.WithRequest(routertest.Request(http.MethodPost, "/request")),
			},
			wantMethod: http.MethodPost,
			wantPath:   "/request",
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

func TestNewContextCarriesTheContextOfTheTest(t *testing.T) {
	c, _ := routertest.NewContext(t, newContext)
	if c.Request().Context() != t.Context() {
		t.Error("the request does not carry t.Context()")
	}

	var inner context.Context
	t.Run("subtest", func(t *testing.T) {
		c, _ := routertest.NewContext(t, newContext)
		inner = c.Request().Context()
		if inner.Err() != nil {
			t.Errorf("context ended while the test runs: %v", inner.Err())
		}
	})
	if inner.Err() == nil {
		t.Error("the context of a subtest is still live after the subtest returned")
	}
}

func TestNewContextWithTarget(t *testing.T) {
	c, _ := routertest.NewContext(t, newContext,
		routertest.WithTarget(http.MethodPost, "/onboarding/?step=2",
			routertest.Host("acme.example.com"),
			routertest.MultipartBody(url.Values{"name": {"acme"}},
				routertest.FilePart{Field: "logo", Content: []byte("png")}),
			routertest.HTMX(),
		))

	if c.Method() != http.MethodPost || c.Path() != "/onboarding/" || c.Query("step") != "2" {
		t.Errorf("request = %s %s ?step=%s", c.Method(), c.Path(), c.Query("step"))
	}
	if c.Host() != "acme.example.com" {
		t.Errorf("host = %q", c.Host())
	}
	if got := c.FormValue("name"); got != "acme" {
		t.Errorf("name = %q, want acme", got)
	}
	f, _, err := c.FormFile("logo")
	if err != nil {
		t.Fatalf("FormFile: %v", err)
	}
	defer func() { _ = f.Close() }()
	if body, _ := io.ReadAll(f); string(body) != "png" {
		t.Errorf("logo = %q, want png", body)
	}
	if c.Request().Header.Get(htmx.HeaderRequest) != "true" {
		t.Error("the htmx header is missing")
	}
	if c.Request().Context() != t.Context() {
		t.Error("the request does not carry t.Context()")
	}
}

func TestWithTargetTakesAContextOfItsOwn(t *testing.T) {
	ctx := context.WithValue(t.Context(), ctxKey{}, "own")
	c, _ := routertest.NewContext(t, newContext,
		routertest.WithTarget(http.MethodGet, "/", routertest.Context(ctx)))
	if c.Request().Context() != ctx {
		t.Error("the context of the option lost to t.Context()")
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
	if _, err := c.ParamAs[string]("absent"); router.StatusOf(err) != http.StatusInternalServerError {
		t.Errorf("ParamAs of a parameter that the options never named = %v, want a 500", err)
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
	res.Expect(t).Status(http.StatusOK)

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
	res.Expect(t).Status(http.StatusOK)

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
	res.Expect(t).Status(http.StatusOK)

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
	res.Expect(t).Status(http.StatusBadRequest)
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
	res.Expect(t).Status(http.StatusOK)

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

// recordingTB keeps what a helper reports instead of failing the test. Its
// Fatalf does not stop, so a helper goes on past a fatal miss.
type recordingTB struct {
	testing.TB
	msg    string
	fatals []string
	errors []string
	failed bool
}

func (tb *recordingTB) Helper() {}

// Context stands in for the context of the test, which NewContext reads before
// it can report anything.
func (tb *recordingTB) Context() context.Context { return context.Background() }

func (tb *recordingTB) Fatalf(format string, args ...any) {
	tb.failed = true
	tb.msg = fmt.Sprintf(format, args...)
	tb.fatals = append(tb.fatals, tb.msg)
}

func (tb *recordingTB) Errorf(format string, args ...any) {
	tb.failed = true
	tb.errors = append(tb.errors, fmt.Sprintf(format, args...))
}

var (
	cookieKey = []byte(strings.Repeat("k", cookie.MinKeyLen))
	codec     = cookie.NewCodec(cookieKey)
)

func signingRouter(r *router.Router[*appContext]) *router.Router[*appContext] {
	r.POST("/signin", func(c *appContext) error {
		if err := codec.Set(c, c.NewCookie("session", "ann", time.Hour)); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
	return r
}

func TestSignedCookieReadsWhatTheRouterSigned(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    *router.Router[*appContext]
	}{
		{"New", router.New(newContext)},
		{"NewPooled", router.NewPooled(func() *appContext { return new(appContext) }, func(c *appContext) { c.DB = "" })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := routertest.Do(signingRouter(tc.r), http.MethodPost, "/signin")

			if v, ok := routertest.SignedCookie(t, res, codec, "session"); !ok || v != "ann" {
				t.Errorf("SignedCookie = %q, %v, want ann, true", v, ok)
			}
		})
	}
}

func TestSignedCookieReportsNothing(t *testing.T) {
	other := cookie.NewCodec([]byte(strings.Repeat("o", cookie.MinKeyLen)))
	tests := []struct {
		name   string
		handle func(c *appContext) error
	}{
		{"the cookie is not set", func(*appContext) error { return nil }},
		{"the cookie is cleared", func(c *appContext) error {
			c.ClearCookie("session")
			return nil
		}},
		{"the cookie is set unsigned", func(c *appContext) error {
			c.SetCookie(c.NewCookie("session", "ann", time.Hour))
			return nil
		}},
		{"the cookie is signed with another codec", func(c *appContext) error {
			return other.Set(c, c.NewCookie("session", "ann", time.Hour))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := router.New(newContext)
			r.POST("/signin", func(c *appContext) error {
				if err := tc.handle(c); err != nil {
					return err
				}
				return c.NoContent(http.StatusNoContent)
			})
			res := routertest.Do(r, http.MethodPost, "/signin")

			if v, ok := routertest.SignedCookie(t, res, codec, "session"); ok {
				t.Errorf("SignedCookie = %q, true, want false", v)
			}
		})
	}

	t.Run("the cookie has expired", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := router.New(newContext)
			r.POST("/signin", func(c *appContext) error {
				if err := codec.Set(c, c.NewCookie("session", "ann", time.Minute)); err != nil {
					return err
				}
				return c.NoContent(http.StatusNoContent)
			})
			res := routertest.Do(r, http.MethodPost, "/signin")

			time.Sleep(2 * time.Minute)

			if v, ok := routertest.SignedCookie(t, res, codec, "session"); ok {
				t.Errorf("SignedCookie = %q, true, want false", v)
			}
		})
	})
}

func TestCookieHelpersFailWithoutACodec(t *testing.T) {
	res := routertest.Do(signingRouter(router.New(newContext)), http.MethodPost, "/signin")
	tests := []struct {
		name string
		want string
		call func(tb testing.TB)
	}{
		{"SignedCookie", "SignedCookie needs a codec", func(tb testing.TB) { routertest.SignedCookie(tb, res, nil, "session") }},
		{"Flashes", "Flashes needs a codec", func(tb testing.TB) { routertest.Flashes(tb, res, nil) }},
		{"FlashCookie", "FlashCookie needs a codec", func(tb testing.TB) { routertest.FlashCookie(tb, nil) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tb := new(recordingTB)
			tc.call(tb)
			if !tb.failed || !strings.Contains(tb.msg, tc.want) {
				t.Errorf("the helper reported %q, failed %v, want a failure that holds %q", tb.msg, tb.failed, tc.want)
			}
		})
	}
}

func flashingRouter(r *router.Router[*appContext]) *router.Router[*appContext] {
	r.POST("/users", func(c *appContext) error {
		for _, f := range []cookie.Flash{{Kind: "success", Message: "user created"}, {Kind: "info", Message: "check your inbox"}} {
			if err := codec.AddFlash(c, f); err != nil {
				return err
			}
		}
		return c.Redirect(http.StatusSeeOther, "/users")
	})
	r.GET("/users", func(c *appContext) error {
		return c.String(http.StatusOK, fmt.Sprintf("%v", codec.Flashes(c)))
	})
	return r
}

func TestFlashesReadsWhatTheHandlerFlashed(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    *router.Router[*appContext]
	}{
		{"New", router.New(newContext)},
		{"NewPooled", router.NewPooled(func() *appContext { return new(appContext) }, func(c *appContext) { c.DB = "" })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := routertest.Do(flashingRouter(tc.r), http.MethodPost, "/users")

			got := routertest.Flashes(t, res, codec)
			want := []cookie.Flash{{Kind: "success", Message: "user created"}, {Kind: "info", Message: "check your inbox"}}
			if !slices.Equal(got, want) {
				t.Errorf("Flashes = %+v, want %+v", got, want)
			}
		})
	}
}

func TestFlashesReportsNothing(t *testing.T) {
	other := cookie.NewCodec([]byte(strings.Repeat("o", cookie.MinKeyLen)))
	tests := []struct {
		name string
		res  func(r *router.Router[*appContext]) *routertest.Response
	}{
		{"the cookie is not set", func(r *router.Router[*appContext]) *routertest.Response {
			return routertest.Get(r, "/users")
		}},
		{"the cookie is cleared", func(r *router.Router[*appContext]) *routertest.Response {
			return routertest.Get(r, "/users", routertest.FlashCookie(t, codec, cookie.Flash{Kind: "info", Message: "seen"}))
		}},
		{"the cookie is signed with another codec", func(r *router.Router[*appContext]) *routertest.Response {
			return routertest.Get(r, "/planted", routertest.FlashCookie(t, other, cookie.Flash{Kind: "info", Message: "planted"}))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := flashingRouter(router.New(newContext))
			r.GET("/planted", func(c *appContext) error {
				ck, err := c.Request().Cookie(cookie.FlashName)
				if err != nil {
					return err
				}
				c.SetCookie(c.NewCookie(cookie.FlashName, ck.Value, time.Minute))
				return c.NoContent(http.StatusNoContent)
			})

			if got := routertest.Flashes(t, tc.res(r), codec); got != nil {
				t.Errorf("Flashes = %+v, want nothing", got)
			}
		})
	}
}

func TestFlashCookieSeedsThePage(t *testing.T) {
	r := flashingRouter(router.New(newContext))

	res := routertest.Get(r, "/users", routertest.FlashCookie(t, codec, cookie.Flash{Kind: "error", Message: "try again"}))

	if got, want := res.String(), "[{error try again}]"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	var cleared bool
	for _, c := range res.Cookies() {
		cleared = cleared || (c.Name == cookie.FlashName && c.MaxAge < 0)
	}
	if !cleared {
		t.Errorf("the page did not clear the flash cookie: %q", res.Header.Values("Set-Cookie"))
	}
}

func TestFlashCookieFailsOnMessagesThatDoNotFit(t *testing.T) {
	tb := new(recordingTB)
	opt := routertest.FlashCookie(tb, codec, cookie.Flash{Kind: "info", Message: strings.Repeat("x", cookie.MaxSize)})

	if !tb.failed || !strings.Contains(tb.msg, cookie.ErrTooLarge.Error()) {
		t.Errorf("FlashCookie reported %q, failed %v, want a failure that names %q", tb.msg, tb.failed, cookie.ErrTooLarge)
	}
	if req := routertest.Request(http.MethodGet, "/", opt); req.Header.Get("Cookie") != "" {
		t.Errorf("the request carries Cookie %q after a failure, want none", req.Header.Get("Cookie"))
	}
}

func TestFlashCookieWithoutMessagesAddsNoCookie(t *testing.T) {
	req := routertest.Request(http.MethodGet, "/", routertest.FlashCookie(t, codec))

	if got := req.Header.Get("Cookie"); got != "" {
		t.Errorf("the request carries Cookie %q, want none", got)
	}
}

func TestFlashesRoundTripThroughFlashCookie(t *testing.T) {
	r := flashingRouter(router.New(newContext))

	created := routertest.Do(r, http.MethodPost, "/users")
	page := routertest.Get(r, "/users", routertest.FlashCookie(t, codec, routertest.Flashes(t, created, codec)...))

	if got, want := page.String(), "[{success user created} {info check your inbox}]"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}
