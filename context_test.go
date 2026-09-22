package router

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newBase(target string) *Base {
	return NewBase(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
}

func TestSchemeValidatesTheForwardedProto(t *testing.T) {
	tests := []struct {
		name    string
		tls     bool
		forward string
		want    string
	}{
		{"a plain request", false, "", "http"},
		{"a TLS connection", true, "", "https"},
		{"the proxy reports https", false, "https", "https"},
		{"the proxy reports http", false, "http", "http"},
		{"any case", false, "HTTPS", "https"},
		{"the first of a chain", false, "https, http", "https"},
		{"the first of a chain is http", false, "http, https", "http"},
		{"spaces around it", false, "  https  ", "https"},
		{"a scheme that is not one of the two", false, "javascript:alert(1)", "http"},
		{"an injected host", false, "https://evil.example.com", "http"},
		{"an empty first element", false, ", https", "http"},
		{"TLS under a header that says http", true, "http", "https"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := "/"
			if tc.tls {
				target = "https://example.com/"
			}
			b := newBase(target)
			if tc.forward != "" {
				b.Request().Header.Set(HeaderXForwardedProto, tc.forward)
			}
			if got := b.Scheme(); got != tc.want {
				t.Errorf("Scheme() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUserAgentAndReferer(t *testing.T) {
	b := newBase("/")
	b.Request().Header.Set(HeaderUserAgent, "curl/8.7.1")
	b.Request().Header.Set(HeaderReferer, "https://example.com/orders")

	if got := b.UserAgent(); got != "curl/8.7.1" {
		t.Errorf("UserAgent() = %q", got)
	}
	if got := b.Referer(); got != "https://example.com/orders" {
		t.Errorf("Referer() = %q", got)
	}

	empty := newBase("/")
	if got := empty.UserAgent(); got != "" {
		t.Errorf("UserAgent() = %q, want empty", got)
	}
	if got := empty.Referer(); got != "" {
		t.Errorf("Referer() = %q, want empty", got)
	}
}

func TestRequestAndResponseAccessors(t *testing.T) {
	deadline := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	req := httptest.NewRequest(http.MethodPatch, "/orders/7?view=full", nil).WithContext(ctx)
	req.Header.Set("X-Test", "value")
	req.Header.Set("Connection", "keep-alive, Upgrade")
	req.Header.Set("Upgrade", "WebSocket")
	req.AddCookie(&http.Cookie{Name: "session", Value: "abc"})
	rec := httptest.NewRecorder()
	b := NewBase(rec, req)

	if got, ok := b.Deadline(); !ok || !got.Equal(deadline) {
		t.Errorf("Deadline() = %s/%v, want %s/true", got, ok, deadline)
	}
	if err := b.Err(); err != nil {
		t.Errorf("Err() = %v before cancellation", err)
	}
	if got := b.Method(); got != http.MethodPatch {
		t.Errorf("Method() = %q, want %q", got, http.MethodPatch)
	}
	if b.URL() != req.URL {
		t.Error("URL() did not return the request URL")
	}
	if got := b.Cookie("session"); got != "abc" {
		t.Fatalf("Cookie(session) = %q, want abc", got)
	}
	if !b.IsWebSocket() {
		t.Error("IsWebSocket() rejected case-insensitive upgrade tokens")
	}
	b.SetCookie(&http.Cookie{Name: "theme", Value: "dark", Path: "/"})
	if got := rec.Header().Get("Set-Cookie"); !strings.Contains(got, "theme=dark") {
		t.Errorf("Set-Cookie = %q, want theme=dark", got)
	}
	cancel()
	if err := b.Err(); !errors.Is(err, context.Canceled) {
		t.Errorf("Err() = %v after cancellation, want context.Canceled", err)
	}
}

func TestSetTestRoutePublishesRouteState(t *testing.T) {
	b := newBase("/users/7")
	b.setTestRoute("/users/{id}", []string{"id"}, []string{"7"})
	if got := b.RoutePattern(); got != "/users/{id}" {
		t.Errorf("RoutePattern() = %q", got)
	}
	if got := b.Param("id"); got != "7" {
		t.Errorf("Param(id) = %q", got)
	}
	if !b.needsCleanup {
		t.Error("setTestRoute did not mark the context for cleanup")
	}
}

func TestRouteMetaIsNilWithoutARouter(t *testing.T) {
	for name, b := range map[string]*Base{"NewBase": newBase("/"), "setTestRoute": newBase("/users/7")} {
		if name == "setTestRoute" {
			b.setTestRoute("/users/{id}", []string{"id"}, []string{"7"})
		}
		if got := b.RouteMeta(); got != nil {
			t.Errorf("%s: RouteMeta() = %v, want nil", name, got)
		}
		if v, ok := MetaAs[string](b); ok {
			t.Errorf("%s: MetaAs[string] = %q, true; want false", name, v)
		}
	}
}

func TestSetTestRouteWithNoPatternLeavesNoRoute(t *testing.T) {
	b := newBase("/users/7")
	b.setTestRoute("/users/{id}", nil, nil)
	b.setTestRoute("", []string{"id"}, []string{"7"})
	if got := b.RoutePattern(); got != "" {
		t.Errorf("RoutePattern() = %q, want none", got)
	}
	if got := b.Param("id"); got != "7" {
		t.Errorf("Param(id) = %q, want %q", got, "7")
	}
}

func TestContextConstructionRejectsNilInputs(t *testing.T) {
	tests := []struct {
		name string
		call func()
	}{
		{name: "response writer", call: func() { NewBase(nil, httptest.NewRequest(http.MethodGet, "/", nil)) }},
		{name: "request", call: func() { NewBase(httptest.NewRecorder(), nil) }},
		{name: "replacement request", call: func() { newBase("/").SetRequest(nil) }},
		{name: "replacement context", call: func() {
			var ctx context.Context
			newBase("/").SetContext(ctx)
		}},
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

func TestQueryHelpersReadTheSameParse(t *testing.T) {
	b := newBase("/search?q=go&empty=")

	if got := b.Query("q"); got != "go" {
		t.Errorf("Query(%q) = %q", "q", got)
	}
	if got := b.QueryAsDefault("empty", "all"); got != "all" {
		t.Errorf("QueryAsDefault of an empty value = %q, want the default", got)
	}
	if got := b.QueryAsDefault("q", "all"); got != "go" {
		t.Errorf("QueryAsDefault(%q) = %q", "q", got)
	}

	b.QueryValues().Set("q", "rust")
	if got := b.Query("q"); got != "rust" {
		t.Errorf("Query(%q) = %q after a change to QueryValues, want the cached parse", "q", got)
	}
}

func TestSetRequestDropsTheParsedQuery(t *testing.T) {
	b := newBase("/search?q=go")
	if got := b.Query("q"); got != "go" {
		t.Fatalf("Query(%q) = %q", "q", got)
	}

	b.SetRequest(httptest.NewRequest(http.MethodGet, "/search?q=rust", nil))
	if got := b.Query("q"); got != "rust" {
		t.Errorf("Query(%q) = %q, want the value of the new request", "q", got)
	}
}

func TestSetRequestDropsTheCachedHost(t *testing.T) {
	b := newBase("/")
	b.Request().Host = "old.example.com"
	if got := b.Host(); got != "old.example.com" {
		t.Fatalf("Host() = %q", got)
	}
	b.hostPattern, b.errIdx = "{tenant}.example.com", 3

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "new.example.net"
	b.SetRequest(req)
	if got := b.Host(); got != "new.example.net" {
		t.Errorf("Host() after SetRequest = %q, want new.example.net", got)
	}
	if b.RouteHost() != "{tenant}.example.com" || b.errIdx != 3 {
		t.Error("SetRequest changed the already-matched route host identity")
	}
}

func TestSetContextAndSetRequestRefuseAContextDerivedFromTheBase(t *testing.T) {
	type key struct{ n int }
	derive := []struct {
		name string
		make func() (*Base, context.Context)
	}{
		{"the Base itself", func() (*Base, context.Context) {
			b := newBase("/")
			return b, b
		}},
		{"a value", func() (*Base, context.Context) {
			b := newBase("/")
			return b, context.WithValue(b, key{1}, "v")
		}},
		{"a cancel", func() (*Base, context.Context) {
			b := newBase("/")
			ctx, cancel := context.WithCancel(b)
			t.Cleanup(cancel)
			return b, ctx
		}},
		{"a timeout", func() (*Base, context.Context) {
			b := newBase("/")
			ctx, cancel := context.WithTimeout(b, time.Hour)
			t.Cleanup(cancel)
			return b, ctx
		}},
		{"no cancel", func() (*Base, context.Context) {
			b := newBase("/")
			return b, context.WithoutCancel(b)
		}},
		{"two levels deep", func() (*Base, context.Context) {
			b := newBase("/")
			return b, context.WithValue(context.WithValue(b, key{1}, "v"), key{2}, "w")
		}},
		{"the embedding context", func() (*Base, context.Context) {
			c := &tctx{Base: *newBase("/")}
			return &c.Base, context.WithValue(c, key{1}, "v")
		}},
	}
	set := []struct {
		name string
		call func(b *Base, ctx context.Context)
	}{
		{"SetRequest", func(b *Base, ctx context.Context) { b.SetRequest(b.Request().WithContext(ctx)) }},
		{"SetContext", func(b *Base, ctx context.Context) { b.SetContext(ctx) }},
	}
	for _, d := range derive {
		for _, s := range set {
			t.Run(s.name+"/"+d.name, func(t *testing.T) {
				b, ctx := d.make()
				old := b.Request()
				func() {
					defer func() {
						msg, _ := recover().(string)
						if !strings.Contains(msg, "Request().Context()") {
							t.Errorf("panic = %q, want one that points to Request().Context()", msg)
						}
					}()
					s.call(b, ctx)
				}()
				if b.Request() != old {
					t.Error("the refused call replaced the request")
				}
				// Each of these would recurse without end had the call gone through.
				_ = b.Value("missing")
				_ = b.Done()
				_ = b.Err()
			})
		}
	}
}

func TestAContextDerivedFromAnotherBaseIsNotALoop(t *testing.T) {
	type key struct{}
	b1, b2 := newBase("/"), newBase("/")

	for name, set := range map[string]func(ctx context.Context){
		"SetRequest": func(ctx context.Context) { b1.SetRequest(b1.Request().WithContext(ctx)) },
		"SetContext": b1.SetContext,
	} {
		set(context.WithValue(b2, key{}, name))
		if got := b1.Value(key{}); got != name {
			t.Errorf("%s: Value = %v, want %s", name, got, name)
		}
		if got, _ := FromContext(b1); got != b1 {
			t.Errorf("%s: FromContext(b1) did not return b1", name)
		}
	}
}

func TestSetContextKeepsTheCachedQueryAndHost(t *testing.T) {
	type key struct{}
	b := newBase("/search?q=go")
	host := b.Host()
	b.QueryValues().Set("q", "cached")

	b.SetContext(context.WithValue(b.Request().Context(), key{}, "v"))
	if got := b.Query("q"); got != "cached" {
		t.Errorf("Query(%q) = %q, want the cached parse", "q", got)
	}
	if got := b.Host(); got != host {
		t.Errorf("Host() = %q, want %q", got, host)
	}
	if b.Value(key{}) != "v" || b.Request().Context().Value(key{}) != "v" {
		t.Error("the new context value is not visible through b and its request")
	}
}

func TestSetContextLeavesTheOldRequestAlone(t *testing.T) {
	type key struct{}
	b := newBase("/")
	old := b.Request()

	b.SetContext(context.WithValue(old.Context(), key{}, "v"))
	if b.Request() == old {
		t.Error("SetContext kept the old request, want a copy")
	}
	if old.Context().Value(key{}) != nil {
		t.Error("SetContext changed the context of the old request")
	}
}

func TestSetContextCarriesCancellation(t *testing.T) {
	b := newBase("/")
	ctx, cancel := context.WithCancel(b.Request().Context())
	b.SetContext(ctx)
	if b.Err() != nil {
		t.Fatalf("Err() = %v before cancel, want nil", b.Err())
	}

	cancel()
	select {
	case <-b.Done():
	default:
		t.Error("Done() is open after cancel")
	}
	if !errors.Is(b.Err(), context.Canceled) {
		t.Errorf("Err() = %v, want context.Canceled", b.Err())
	}
}

func TestParamNamesReturnsACopy(t *testing.T) {
	b := newBase("/")
	b.setRoute(&routeRecord{pattern: "/users/{id}"}, []string{"id"}, []string{"7"})
	names := b.ParamNames()
	names[0] = "corrupt"
	if got := b.Param("id"); got != "7" {
		t.Errorf("Param(id) = %q after caller mutated ParamNames", got)
	}
	if got := b.ParamNames()[0]; got != "id" {
		t.Errorf("ParamNames()[0] = %q, want id", got)
	}
}

func TestInitDropsTheParsedQuery(t *testing.T) {
	b := newBase("/search?q=go")
	if got := b.Query("q"); got != "go" {
		t.Fatalf("Query(%q) = %q", "q", got)
	}
	b.init(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/search", nil))
	if got := b.Query("q"); got != "" {
		t.Errorf("Query(%q) = %q, want empty; the cache outlived the request", "q", got)
	}
}

func TestAccepts(t *testing.T) {
	html, jsn, text := MIMETextHTML, MIMEApplicationJSON, MIMETextPlain

	tests := []struct {
		name   string
		accept string
		offers []string
		want   string
	}{
		{"no header takes the first offer", "", []string{html, jsn}, html},
		{"an exact type", "application/json", []string{html, jsn}, jsn},
		{"the type it prefers", "text/html;q=0.9, application/json;q=1.0", []string{html, jsn}, jsn},
		{"a q value that loses", "text/html, application/json;q=0.2", []string{jsn, html}, html},
		{"a subtype wildcard", "text/*", []string{jsn, html}, html},
		{"a full wildcard", "*/*", []string{jsn, html}, jsn},
		{"a type it refuses", "text/html", []string{jsn}, ""},
		{"q=0 refuses the type", "application/json;q=0", []string{jsn}, ""},
		{"the specific range wins over the wildcard", "text/*;q=0.9, text/plain;q=0", []string{text, html}, html},
		{"a browser header", "text/html,application/xhtml+xml,*/*;q=0.8", []string{jsn, html}, html},
		{"case does not matter", "APPLICATION/JSON", []string{jsn}, jsn},
		{"an offer with parameters", "application/json", []string{MIMEApplicationJSONCharsetUTF8}, MIMEApplicationJSONCharsetUTF8},
		{"no offers", "*/*", nil, ""},
		{"a malformed header", "garbage", []string{jsn}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newBase("/")
			if tc.accept != "" {
				b.Request().Header.Set(HeaderAccept, tc.accept)
			}
			if got := b.Accepts(tc.offers...); got != tc.want {
				t.Errorf("Accepts(%v) with Accept %q = %q, want %q", tc.offers, tc.accept, got, tc.want)
			}
		})
	}
}

func TestLoggerFallsBackToTheDefault(t *testing.T) {
	b := newBase("/")
	if b.Logger() != slog.Default() {
		t.Error("Logger() answered with something other than the default logger")
	}

	own := slog.New(slog.DiscardHandler)
	b.ropts = &routerOpts{logger: own}
	if b.Logger() != own {
		t.Error("Logger() did not answer with the logger of the router")
	}
}

func TestRouterSettingsReachTheContext(t *testing.T) {
	own := slog.New(slog.DiscardHandler)

	r := newTestRouter()
	r.MaxBodyBytes(1 << 20)
	r.MaxMultipartMemory(64 << 10)
	r.Logger(own)

	var got *Base
	r.GET("/", func(c *tctx) error {
		got = c.base()
		return c.NoContent(http.StatusNoContent)
	})
	do(r, http.MethodGet, "/")

	if got == nil {
		t.Fatal("the handler did not run")
	}
	opts := got.opts()
	if opts.maxBody != 1<<20 {
		t.Errorf("maxBody = %d, want %d", opts.maxBody, 1<<20)
	}
	if opts.maxMultipart != 64<<10 {
		t.Errorf("maxMultipart = %d, want %d", opts.maxMultipart, 64<<10)
	}
	if got.Logger() != own {
		t.Error("Logger() did not answer with the logger of the router")
	}
}

func TestNewSettingsAfterServingPanic(t *testing.T) {
	tests := []struct {
		name string
		set  func(r *Router[*tctx])
	}{
		{"the multipart memory limit", func(r *Router[*tctx]) { r.MaxMultipartMemory(1) }},
		{"the logger", func(r *Router[*tctx]) { r.Logger(slog.New(slog.DiscardHandler)) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRouter()
			r.GET("/a", echoRoute)
			do(r, http.MethodGet, "/a")

			defer func() {
				msg, _ := recover().(string)
				if !strings.Contains(msg, "after the router started serving") {
					t.Errorf("panic = %q, want one that names the state of the router", msg)
				}
				if !strings.Contains(msg, tc.name) {
					t.Errorf("panic = %q, want one that names %q", msg, tc.name)
				}
			}()
			tc.set(r)
		})
	}
}

func TestDeferredStateDoesNotClobberItself(t *testing.T) {
	b := NewBase(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if got := b.formError(); got != nil {
		t.Errorf("formError() = %v, want nil before it is set", got)
	}
	if b.deferred != nil {
		t.Error("deferred is allocated before any failure")
	}

	form := ErrBadRequest.WithMessage("form")
	if got := b.setFormError(form); !errors.Is(got, form) {
		t.Errorf("setFormError returned %v, want the failure it recorded", got)
	}
	b.deferrals().bodyLimit = 5

	if got := b.formError(); !errors.Is(got, form) {
		t.Errorf("formError() = %v, want the form failure", got)
	}
	if got := b.deferred.bodyLimit; got != 5 {
		t.Errorf("bodyLimit = %d, want 5", got)
	}

	b.init(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if b.deferred != nil || b.formError() != nil {
		t.Error("init left a deferred failure behind, which a pooled context would carry on")
	}
}

func TestVary(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		c.Vary("Hx-Request", "")
		c.Vary("hx-request")
		c.Vary(HeaderAccept)
		return c.NoContent(http.StatusOK)
	})

	got := do(r, http.MethodGet, "/").Header().Values(HeaderVary)
	want := []string{"Hx-Request", HeaderAccept}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Vary = %v, want %v", got, want)
	}
}

func TestVarySeesAListThatOneHeaderHolds(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		c.SetHeader(HeaderVary, "Accept, HX-Request")
		c.Vary("Hx-Request")
		return c.NoContent(http.StatusOK)
	})

	if got := do(r, http.MethodGet, "/").Header().Values(HeaderVary); len(got) != 1 {
		t.Errorf("Vary = %v, want the one header that the handler set", got)
	}
}

func TestAddVarySkipsWhatTheHeaderAlreadyNames(t *testing.T) {
	h := http.Header{}
	AddVary(h, HeaderCookie)
	AddVary(h, HeaderCookie, "cookie", "")
	if got := h.Values(HeaderVary); len(got) != 1 || got[0] != HeaderCookie {
		t.Errorf("Vary = %v, want one %q", got, HeaderCookie)
	}

	h = http.Header{"Vary": {"*"}}
	AddVary(h, HeaderCookie)
	if got := h.Values(HeaderVary); len(got) != 1 || got[0] != "*" {
		t.Errorf("Vary = %v, want the wildcard alone", got)
	}
}
