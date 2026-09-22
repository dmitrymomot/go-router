package htmx

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
)

type tctx struct{ router.Base }

func newRouter() *router.Router[*tctx] {
	return router.New(func(http.ResponseWriter, *http.Request) *tctx { return new(tctx) })
}

func do(h http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func comp(s string) router.ComponentFunc {
	return func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, s)
		return err
	}
}

// hxOnly is what htmx sends when the request names nothing else.
var hxOnly = map[string]string{HeaderRequest: "true"}

var hxVary = []string{HeaderRequest, HeaderRequestType}

var htmxHeaders = map[string]string{
	HeaderRequest:               "true",
	HeaderRequestType:           "partial",
	HeaderBoosted:               "true",
	HeaderCurrentURL:            "https://example.com/chat",
	HeaderHistoryRestoreRequest: "true",
	HeaderSource:                "button#send",
	HeaderTarget:                "ul#log",
}

func requestWith(headers map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func TestRequestOf(t *testing.T) {
	got := RequestOf(requestWith(htmxHeaders))

	want := Request{
		CurrentURL:     "https://example.com/chat",
		RequestType:    "partial",
		Source:         "button#send",
		Target:         "ul#log",
		Request:        true,
		Boosted:        true,
		HistoryRestore: true,
	}
	if got != want {
		t.Errorf("RequestOf() = %+v, want %+v", got, want)
	}
}

func TestRequestOfEmpty(t *testing.T) {
	if got := RequestOf(requestWith(nil)); got != (Request{}) {
		t.Errorf("RequestOf() = %+v, want the zero value", got)
	}
}

func TestRequestOfWithoutHXRequestIsZero(t *testing.T) {
	for _, value := range []string{"", "false", "1"} {
		headers := maps.Clone(htmxHeaders)
		if value == "" {
			delete(headers, HeaderRequest)
		} else {
			headers[HeaderRequest] = value
		}
		if got := RequestOf(requestWith(headers)); got != (Request{}) {
			t.Errorf("HX-Request %q: RequestOf() = %+v, want the zero value", value, got)
		}
	}
}

func TestRequestTargetID(t *testing.T) {
	for _, tc := range idCases {
		if got := (Request{Target: tc.in}).TargetID(); got != tc.want {
			t.Errorf("TargetID() of %q = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRequestSourceID(t *testing.T) {
	for _, tc := range idCases {
		if got := (Request{Source: tc.in}).SourceID(); got != tc.want {
			t.Errorf("SourceID() of %q = %q, want %q", tc.in, got, tc.want)
		}
	}
}

var idCases = []struct{ in, want string }{
	{"ul#user-list", "user-list"},
	{"div", ""},
	{"body", ""},
	{"", ""},
	{"#list", "list"},
	{"li#row%207", "row 7"},
	{"div#%D0%BA%D0%BB", "кл"},
	{"div#50%", "50%"},
	{"div#a#b", "a#b"},
	{"div#a+b", "a+b"},
}

func TestRequestOfIgnoresCase(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		request bool
		boosted bool
	}{
		{"lower case", map[string]string{HeaderRequest: "true"}, true, false},
		{"upper case", map[string]string{HeaderRequest: "TRUE"}, true, false},
		{"boosted", map[string]string{HeaderRequest: "true", HeaderBoosted: "true"}, true, true},
		{"a value that is not true", map[string]string{HeaderRequest: "1"}, false, false},
		{"no header", nil, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RequestOf(requestWith(tc.headers))
			if got.Request != tc.request || got.Boosted != tc.boosted {
				t.Errorf("Request, Boosted = %v, %v, want %v, %v", got.Request, got.Boosted, tc.request, tc.boosted)
			}
		})
	}
}

func TestHeaderSpelling(t *testing.T) {
	for _, name := range []string{
		HeaderRequest, HeaderRequestType, HeaderBoosted, HeaderCurrentURL,
		HeaderHistoryRestoreRequest, HeaderSource, HeaderTarget,
		HeaderTrigger, HeaderLocation,
		HeaderPushURL, HeaderRedirect, HeaderRefresh, HeaderReplaceURL,
		HeaderReswap, HeaderRetarget, HeaderReselect,
	} {
		if got := http.CanonicalHeaderKey(name); got != name {
			t.Errorf("the constant %q is not canonical, want %q", name, got)
		}
	}

	h := http.Header{}
	h.Set("HX-Request", "true")
	if !isTrue(h.Get(HeaderRequest)) {
		t.Error("a header that the client spelled HX-Request did not answer the constant")
	}
}

func TestPartial(t *testing.T) {
	r := newRouter()
	r.GET("/", Partial(
		func(c *tctx) error { return c.String(http.StatusOK, "partial") },
		func(c *tctx) error { return c.String(http.StatusOK, "page") },
	))

	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"a browser gets the page", nil, "page"},
		{"htmx gets the partial", hxOnly, "partial"},
		{
			"a partial request gets the partial",
			map[string]string{HeaderRequest: "true", HeaderRequestType: "partial"},
			"partial",
		},
		{
			"a boosted request gets the page",
			map[string]string{HeaderRequest: "true", HeaderRequestType: "full", HeaderBoosted: "true"},
			"page",
		},
		{
			"a history restore gets the page",
			map[string]string{HeaderRequest: "true", HeaderRequestType: "full", HeaderHistoryRestoreRequest: "true"},
			"page",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(r, http.MethodGet, "/", tc.headers)
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
			if got := rec.Header().Values(router.HeaderVary); !slices.Equal(got, hxVary) {
				t.Errorf("Vary = %v, want %v", got, hxVary)
			}
		})
	}
}

func TestWantsPartialSetsVary(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		vary    string
		want    []string
	}{
		{"a browser", nil, "", hxVary},
		{"a partial request", map[string]string{HeaderRequest: "true", HeaderRequestType: "partial"}, "", hxVary},
		{"a full request", map[string]string{HeaderRequest: "true", HeaderRequestType: "full"}, "", hxVary},
		{"an answer that varies on everything", hxOnly, "*", []string{"*"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if tc.vary != "" {
				rec.Header().Set(router.HeaderVary, tc.vary)
			}
			b := router.NewBase(rec, requestWith(tc.headers))

			WantsPartial(b)
			WantsPartial(b)
			if got := rec.Header().Values(router.HeaderVary); !slices.Equal(got, tc.want) {
				t.Errorf("Vary after two calls = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRenderPartial(t *testing.T) {
	r := newRouter()
	r.GET("/", func(c *tctx) error {
		return RenderPartial(c, http.StatusOK, comp("<li>card</li>"), comp("<h1>page</h1>"))
	})
	r.GET("/invalid", func(c *tctx) error {
		return RenderPartial(c, http.StatusUnprocessableEntity, comp("<form>partial</form>"), comp("<form>page</form>"))
	})
	r.GET("/broken", func(c *tctx) error {
		broken := router.ComponentFunc(func(context.Context, io.Writer) error { return errors.New("template failed") })
		return RenderPartial(c, http.StatusOK, broken, broken)
	})

	const (
		partial = "<li>card</li>"
		page    = "<h1>page</h1>"
	)
	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"a browser gets the page", nil, page},
		{"a partial request gets the partial", map[string]string{HeaderRequest: "true", HeaderRequestType: "partial"}, partial},
		{
			"a full boosted request gets the page",
			map[string]string{HeaderRequest: "true", HeaderRequestType: "full", HeaderBoosted: "true"},
			page,
		},
		{
			"a full history restore gets the page",
			map[string]string{HeaderRequest: "true", HeaderRequestType: "full", HeaderHistoryRestoreRequest: "true"},
			page,
		},
		{"a request with no type gets the partial", hxOnly, partial},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(r, http.MethodGet, "/", tc.headers)
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
			if got := rec.Header().Get(router.HeaderContentType); got != router.MIMETextHTMLCharsetUTF8 {
				t.Errorf("Content-Type = %q, want %q", got, router.MIMETextHTMLCharsetUTF8)
			}
			if got := rec.Header().Values(router.HeaderVary); !slices.Equal(got, hxVary) {
				t.Errorf("Vary = %v, want %v", got, hxVary)
			}
		})
	}

	t.Run("both branches keep the status", func(t *testing.T) {
		for _, headers := range []map[string]string{nil, hxOnly} {
			rec := do(r, http.MethodGet, "/invalid", headers)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422", rec.Code)
			}
		}
	})

	t.Run("HEAD writes no body", func(t *testing.T) {
		rec := do(r, http.MethodHead, "/", hxOnly)
		if rec.Body.Len() != 0 {
			t.Errorf("body = %q, want an empty one", rec.Body.String())
		}
	})

	t.Run("a failing component answers 500", func(t *testing.T) {
		for _, headers := range []map[string]string{nil, hxOnly} {
			if code := do(r, http.MethodGet, "/broken", headers).Code; code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", code)
			}
		}
	})
}

func TestPartialNeedsBothHandlers(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Partial(nil, nil) did not panic")
		}
	}()
	_ = Partial[*tctx](nil, nil)
}

func TestResponseHeaders(t *testing.T) {
	r := newRouter()
	r.GET("/", func(c *tctx) error {
		hx := NewResponse(c).
			PushURL("/rows/7").
			ReplaceURL("/rows/7?edit").
			Retarget("#row-7").
			Reselect("#row-7 td").
			Reswap(SwapOuterHTML).
			Refresh().
			Trigger("saved", "closed")
		if err := hx.Err(); err != nil {
			return err
		}
		return c.Render(http.StatusOK, comp("<tr></tr>"))
	})

	rec := do(r, http.MethodGet, "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "<tr></tr>" {
		t.Errorf("body = %q, want %q", got, "<tr></tr>")
	}
	for _, tc := range []struct{ header, want string }{
		{HeaderPushURL, "/rows/7"},
		{HeaderReplaceURL, "/rows/7?edit"},
		{HeaderRetarget, "#row-7"},
		{HeaderReselect, "#row-7 td"},
		{HeaderReswap, "outerHTML"},
		{HeaderRefresh, "true"},
		{HeaderTrigger, "saved, closed"},
	} {
		if got := rec.Header().Get(tc.header); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.header, got, tc.want)
		}
	}
}

func TestTriggerWithoutNamesWritesNothing(t *testing.T) {
	r := newRouter()
	r.GET("/", func(c *tctx) error {
		return NewResponse(c).Trigger().TriggerEvents().NoSwap()
	})

	rec := do(r, http.MethodGet, "/", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if _, ok := rec.Header()[HeaderTrigger]; ok {
		t.Errorf("%s = %q, want no header at all", HeaderTrigger, rec.Header().Get(HeaderTrigger))
	}
}

func TestTriggerRejectsANameThatTheHeaderCannotCarry(t *testing.T) {
	tests := []struct {
		name  string
		event string
		want  error
	}{
		{"empty", "", errEmptyEventName},
		{"a comma", "a,b", errEventNameSeparator},
		{"a line break", "a\nb", errEventNameSeparator},
		{"outside ASCII", "gespeichert-ü", errEventNameNotASCII},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRouter()
			r.GET("/", func(c *tctx) error {
				err := NewResponse(c).Trigger(tc.event).Err()
				if !errors.Is(err, tc.want) {
					t.Errorf("Err() = %v, want one that wraps %v", err, tc.want)
				}
				return NewResponse(c).Trigger(tc.event).NoSwap()
			})

			rec := do(r, http.MethodGet, "/", nil)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if got := rec.Header().Get(HeaderTrigger); got != "" {
				t.Errorf("%s = %q, want no header", HeaderTrigger, got)
			}
		})
	}
}

func TestTriggerEvents(t *testing.T) {
	r := newRouter()
	r.GET("/", func(c *tctx) error {
		return NewResponse(c).
			TriggerEvents(
				Event{Name: "toast", Detail: "Gespeichert"},
				Event{Name: "count", Detail: map[string]int{"rows": 7}},
				Event{Name: "plain"},
			).
			NoSwap()
	})

	got := do(r, http.MethodGet, "/", nil).Header().Get(HeaderTrigger)
	want := `{"toast":"Gespeichert","count":{"rows":7},"plain":null}`
	if got != want {
		t.Errorf("%s = %q, want %q", HeaderTrigger, got, want)
	}
}

func TestTriggerEventsEscapeEveryCharacterOutsideASCII(t *testing.T) {
	r := newRouter()
	r.GET("/", func(c *tctx) error {
		return NewResponse(c).TriggerEvents(Event{Name: "toast", Detail: "über 😀"}).NoSwap()
	})

	got := do(r, http.MethodGet, "/", nil).Header().Get(HeaderTrigger)
	want := `{"toast":"\u00fcber \ud83d\ude00"}`
	if got != want {
		t.Errorf("%s = %q, want %q", HeaderTrigger, got, want)
	}
	if !isASCII(got) {
		t.Errorf("%s = %q, which is not ASCII", HeaderTrigger, got)
	}
}

func TestTriggerEventsNeedsANameForEveryEvent(t *testing.T) {
	r := newRouter()
	r.GET("/", func(c *tctx) error {
		return NewResponse(c).TriggerEvents(Event{Detail: 1}).NoSwap()
	})

	if code := do(r, http.MethodGet, "/", nil).Code; code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", code)
	}
}

func TestTriggerEventsReportsADetailThatCannotEncode(t *testing.T) {
	r := newRouter()
	r.GET("/", func(c *tctx) error {
		return NewResponse(c).TriggerEvents(Event{Name: "bad", Detail: make(chan int)}).NoSwap()
	})

	rec := do(r, http.MethodGet, "/", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get(HeaderTrigger); got != "" {
		t.Errorf("%s = %q, want no header", HeaderTrigger, got)
	}
}

func TestResponseRejectsALineBreakInAHeader(t *testing.T) {
	r := newRouter()
	r.GET("/", func(c *tctx) error {
		return NewResponse(c).Retarget("#a\r\nHX-Redirect: /evil").NoSwap()
	})

	rec := do(r, http.MethodGet, "/", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get(HeaderRetarget); got != "" {
		t.Errorf("%s = %q, want no header", HeaderRetarget, got)
	}
}

func TestResponseRejectsAByteAHeaderCannotCarry(t *testing.T) {
	for _, value := range []string{
		"#a\x00b", "#a\tb", "#a\x1fb", "#a\x7fb", "#a\x80b", "#ü", "/über",
	} {
		t.Run(value, func(t *testing.T) {
			b := router.NewBase(httptest.NewRecorder(), requestWith(nil))
			hx := NewResponse(b).Retarget(value).PushURL(value)
			if !errors.Is(hx.Err(), errHeaderByte) {
				t.Errorf("Err() = %v, want one that wraps %v", hx.Err(), errHeaderByte)
			}
			for _, name := range []string{HeaderRetarget, HeaderPushURL} {
				if got := b.Response().Header().Get(name); got != "" {
					t.Errorf("%s = %q, want no header", name, got)
				}
			}
		})
	}

	b := router.NewBase(httptest.NewRecorder(), requestWith(nil))
	if err := NewResponse(b).Retarget("#a ~b > c[x='~!@#$%^&*()']").Err(); err != nil {
		t.Errorf("Err() = %v for printable ASCII, want nil", err)
	}
}

func TestResponseKeepsTheFirstFailure(t *testing.T) {
	r := newRouter()
	r.GET("/", func(c *tctx) error {
		err := NewResponse(c).Retarget("a\nb").Trigger("also,bad").Err()
		if err == nil || !strings.Contains(err.Error(), HeaderRetarget) {
			t.Errorf("Err() = %v, want the failure of Retarget", err)
		}
		return c.NoContent(http.StatusOK)
	})

	do(r, http.MethodGet, "/", nil)
}

func TestAFailedResponseLeavesTheNextOneAlone(t *testing.T) {
	r := newRouter()
	r.GET("/", func(c *tctx) error {
		_ = NewResponse(c).Trigger("a,b").Err()
		hx := NewResponse(c).PushURL("/x")
		if err := hx.Err(); err != nil {
			return err
		}
		return c.String(http.StatusOK, "ok")
	})

	rec := do(r, http.MethodGet, "/", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get(HeaderPushURL); got != "/x" {
		t.Errorf("%s = %q, want %q", HeaderPushURL, got, "/x")
	}
}

func TestNoSwap(t *testing.T) {
	r := newRouter()
	r.POST("/messages", func(c *tctx) error {
		return NewResponse(c).Trigger("message-sent").NoSwap()
	})

	rec := do(r, http.MethodPost, "/messages", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want an empty one", rec.Body.String())
	}
	if got := rec.Header().Get(HeaderTrigger); got != "message-sent" {
		t.Errorf("%s = %q, want %q", HeaderTrigger, got, "message-sent")
	}
}

func TestRedirect(t *testing.T) {
	r := newRouter()
	r.POST("/join", func(c *tctx) error { return NewResponse(c).Redirect("/chat") })

	t.Run("htmx gets a client-side redirect", func(t *testing.T) {
		rec := do(r, http.MethodPost, "/join", hxOnly)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get(HeaderRedirect); got != "/chat" {
			t.Errorf("%s = %q, want %q", HeaderRedirect, got, "/chat")
		}
		if got := rec.Header().Get(router.HeaderLocation); got != "" {
			t.Errorf("%s = %q, want no header", router.HeaderLocation, got)
		}
	})

	t.Run("a browser gets a 303", func(t *testing.T) {
		rec := do(r, http.MethodPost, "/join", nil)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rec.Code)
		}
		if got := rec.Header().Get(router.HeaderLocation); got != "/chat" {
			t.Errorf("%s = %q, want %q", router.HeaderLocation, got, "/chat")
		}
		if got := rec.Header().Get(HeaderRedirect); got != "" {
			t.Errorf("%s = %q, want no header", HeaderRedirect, got)
		}
	})
}

func TestNavigationNeedsAURL(t *testing.T) {
	ends := map[string]func(hx *Response) error{
		"Redirect":     func(hx *Response) error { return hx.Redirect("") },
		"Location":     func(hx *Response) error { return hx.Location("") },
		"LocationWith": func(hx *Response) error { return hx.LocationWith(Location{Target: "#main"}) },
	}
	for name, end := range ends {
		for who, headers := range map[string]map[string]string{"a browser": nil, "htmx": hxOnly} {
			t.Run(name+" for "+who, func(t *testing.T) {
				rec := httptest.NewRecorder()
				hx := NewResponse(router.NewBase(rec, requestWith(headers)))
				if err := end(hx); err == nil || !errors.Is(hx.Err(), err) {
					t.Errorf("%s(\"\") = %v, want the failure that Err reports", name, err)
				}
				if rec.Code != http.StatusOK || rec.Flushed || len(rec.Header()) != 0 {
					t.Errorf("wrote %d with header %v, want nothing written", rec.Code, rec.Header())
				}
			})
		}
	}
}

func TestRedirectCarriesACookie(t *testing.T) {
	r := newRouter()
	r.POST("/join", func(c *tctx) error {
		c.SetCookie(c.NewCookie("seen", "welcome", time.Hour))
		return NewResponse(c).Redirect("/chat")
	})
	r.GET("/chat", func(c *tctx) error { return c.String(http.StatusOK, c.Cookie("seen")) })

	for _, tc := range []struct {
		name    string
		headers map[string]string
		status  int
	}{
		{"htmx gets a client-side redirect", hxOnly, http.StatusOK},
		{"a browser gets a 303", nil, http.StatusSeeOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(r, http.MethodPost, "/join", tc.headers)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			line := rec.Header().Get(router.HeaderSetCookie)
			if !strings.HasPrefix(line, "seen=") {
				t.Fatalf("the redirect carries Set-Cookie %q, want the cookie the handler set", line)
			}
			ck, err := http.ParseSetCookie(line)
			if err != nil {
				t.Fatalf("the Set-Cookie does not parse: %q: %v", line, err)
			}

			sent := (&http.Cookie{Name: ck.Name, Value: ck.Value}).String()
			got := do(r, http.MethodGet, "/chat", map[string]string{router.HeaderCookie: sent})
			if want := "welcome"; got.Body.String() != want {
				t.Errorf("the next page shows %q, want %q", got.Body, want)
			}
		})
	}
}

func TestLocation(t *testing.T) {
	r := newRouter()
	r.GET("/short", func(c *tctx) error { return NewResponse(c).Location("/chat") })
	r.GET("/full", func(c *tctx) error {
		return NewResponse(c).LocationWith(Location{
			Path:   "/chat",
			Target: "#main",
			Swap:   SwapInnerHTML,
			Values: map[string]string{"room": "go"},
		})
	})
	r.GET("/select", func(c *tctx) error {
		return NewResponse(c).LocationWith(Location{Path: "/chat", Select: "#messages"})
	})
	r.GET("/path-only", func(c *tctx) error {
		return NewResponse(c).LocationWith(Location{Path: "/chat"})
	})
	r.GET("/no-path", func(c *tctx) error { return NewResponse(c).LocationWith(Location{}) })

	tests := []struct {
		name, target, want string
	}{
		{"a path alone stays a URL", "/short", "/chat"},
		{"a location that is only a path stays a URL", "/path-only", "/chat"},
		{
			"a location with a context becomes JSON", "/full",
			`{"path":"/chat","target":"#main","swap":"innerHTML","values":{"room":"go"}}`,
		},
		{"a location with a select becomes JSON", "/select", `{"path":"/chat","select":"#messages"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(r, http.MethodGet, tc.target, hxOnly)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get(HeaderLocation); got != tc.want {
				t.Errorf("%s = %q, want %q", HeaderLocation, got, tc.want)
			}
		})
	}

	t.Run("a browser gets a 303", func(t *testing.T) {
		for _, target := range []string{"/short", "/full"} {
			rec := do(r, http.MethodGet, target, nil)
			if rec.Code != http.StatusSeeOther {
				t.Errorf("%s: status = %d, want 303", target, rec.Code)
			}
			if got := rec.Header().Get(router.HeaderLocation); got != "/chat" {
				t.Errorf("%s: %s = %q, want %q", target, router.HeaderLocation, got, "/chat")
			}
		}
	})

	t.Run("a location needs a path", func(t *testing.T) {
		if code := do(r, http.MethodGet, "/no-path", hxOnly).Code; code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", code)
		}
	})
}

func TestNavigationReportsAFailedChain(t *testing.T) {
	bad := func(c *tctx) *Response { return NewResponse(c).Retarget("a\nb") }

	ends := map[string]func(c *tctx) error{
		"NoSwap":       func(c *tctx) error { return bad(c).NoSwap() },
		"Redirect":     func(c *tctx) error { return bad(c).Redirect("/x") },
		"Location":     func(c *tctx) error { return bad(c).Location("/x") },
		"LocationWith": func(c *tctx) error { return bad(c).LocationWith(Location{Path: "/x"}) },
	}
	for name, end := range ends {
		t.Run(name, func(t *testing.T) {
			r := newRouter()
			r.GET("/", end)
			if code := do(r, http.MethodGet, "/", hxOnly).Code; code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", code)
			}
		})
	}
}

func TestEscapeNonASCII(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{`{"a":"b"}`, `{"a":"b"}`},
		{`{"a":"ü"}`, `{"a":"\u00fc"}`},
		{`{"a":"😀"}`, `{"a":"\ud83d\ude00"}`},
		{`{"ключ":1}`, `{"\u043a\u043b\u044e\u0447":1}`},
	}
	for _, tc := range tests {
		if got := escapeNonASCII(tc.in); got != tc.want {
			t.Errorf("escapeNonASCII(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResponseStopsAtTheFirstFailedHeader(t *testing.T) {
	r := newRouter()
	r.GET("/", func(c *tctx) error {
		return NewResponse(c).Retarget("a\nb").PushURL("/x").NoSwap()
	})

	rec := do(r, http.MethodGet, "/", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get(HeaderPushURL); got != "" {
		t.Errorf("%s = %q, want no header, because the chain had already failed", HeaderPushURL, got)
	}
}

func TestRedirectRejectsALineBreakInTheURL(t *testing.T) {
	r := newRouter()
	r.GET("/redirect", func(c *tctx) error { return NewResponse(c).Redirect("/a\r\nHX-Refresh: true") })
	r.GET("/location", func(c *tctx) error { return NewResponse(c).Location("/a\r\nHX-Refresh: true") })

	for _, target := range []string{"/redirect", "/location"} {
		t.Run(target, func(t *testing.T) {
			rec := do(r, http.MethodGet, target, hxOnly)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if got := rec.Header().Get(HeaderRefresh); got != "" {
				t.Errorf("%s = %q, want no header", HeaderRefresh, got)
			}
		})
	}
}

func TestResponseRejectsInvalidUTF8(t *testing.T) {
	r := newRouter()
	r.GET("/trigger", func(c *tctx) error {
		return NewResponse(c).TriggerEvents(Event{Name: "\xff", Detail: 1}).NoSwap()
	})
	r.GET("/location", func(c *tctx) error {
		return NewResponse(c).LocationWith(Location{Path: "/chat", Values: map[string]string{"\xff": "x"}})
	})

	for _, target := range []string{"/trigger", "/location"} {
		t.Run(target, func(t *testing.T) {
			rec := do(r, http.MethodGet, target, hxOnly)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
		})
	}
}

func TestResponseReportsAFailureOfADroppedLink(t *testing.T) {
	r := newRouter()
	r.GET("/", func(c *tctx) error {
		hx := NewResponse(c)
		hx.Retarget("bad\nvalue")
		if err := hx.Err(); err != nil {
			return err
		}
		return c.Render(http.StatusOK, comp("x"))
	})

	rec := do(r, http.MethodGet, "/", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get(HeaderRetarget); got != "" {
		t.Errorf("%s = %q, want no header", HeaderRetarget, got)
	}
}

func TestTriggerRejectsARepeatedName(t *testing.T) {
	ends := map[string]func(c *tctx) error{
		"Trigger": func(c *tctx) error {
			return NewResponse(c).Trigger("saved", "saved").NoSwap()
		},
		"TriggerEvents": func(c *tctx) error {
			return NewResponse(c).TriggerEvents(
				Event{Name: "saved", Detail: 1},
				Event{Name: "saved", Detail: 2},
			).NoSwap()
		},
	}
	for name, end := range ends {
		t.Run(name, func(t *testing.T) {
			r := newRouter()
			r.GET("/", end)

			rec := do(r, http.MethodGet, "/", nil)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if got := rec.Header().Get(HeaderTrigger); got != "" {
				t.Errorf("%s = %q, want no header", HeaderTrigger, got)
			}
		})
	}
}

func TestWantsPartial(t *testing.T) {
	hx := func(kv ...string) map[string]string {
		h := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			h[kv[i]] = kv[i+1]
		}
		return h
	}
	tests := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"no headers", nil, false},
		{"htmx with no type", hx(HeaderRequest, "true"), true},
		{"partial", hx(HeaderRequest, "true", HeaderRequestType, "partial"), true},
		{"PARTIAL", hx(HeaderRequest, "true", HeaderRequestType, "PARTIAL"), true},
		{"full", hx(HeaderRequest, "true", HeaderRequestType, "full"), false},
		{"FULL", hx(HeaderRequest, "true", HeaderRequestType, "FULL"), false},
		{
			"full and boosted",
			hx(HeaderRequest, "true", HeaderRequestType, "full", HeaderBoosted, "true"),
			false,
		},
		{
			"partial and boosted",
			hx(HeaderRequest, "true", HeaderRequestType, "partial", HeaderBoosted, "true"),
			true,
		},
		{
			"full and a history restore",
			hx(HeaderRequest, "true", HeaderRequestType, "full", HeaderHistoryRestoreRequest, "true"),
			false,
		},
		{"htmx 2 boosted with no type", hx(HeaderRequest, "true", HeaderBoosted, "true"), true},
		{"a type without HX-Request", hx(HeaderRequestType, "partial"), false},
		{"an HX-Request that is not true", hx(HeaderRequest, "1"), false},
		{"a type htmx does not send", hx(HeaderRequest, "true", HeaderRequestType, "sideways"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := router.NewBase(httptest.NewRecorder(), requestWith(tc.headers))
			if got := WantsPartial(b); got != tc.want {
				t.Errorf("WantsPartial() = %v, want %v", got, tc.want)
			}
		})
	}
}

type nopWriter struct{ h http.Header }

func (w *nopWriter) Header() http.Header         { return w.h }
func (w *nopWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *nopWriter) WriteHeader(int)             {}

func BenchmarkResponse(b *testing.B) {
	r := newRouter()
	r.GET("/rows/{id}", func(c *tctx) error {
		return NewResponse(c).Retarget("#row").Reswap(SwapOuterHTML).Trigger("saved").NoSwap()
	})
	w := &nopWriter{h: make(http.Header)}
	req := httptest.NewRequest(http.MethodGet, "/rows/7", nil)

	b.ReportAllocs()
	for b.Loop() {
		r.ServeHTTP(w, req)
	}
}

func BenchmarkWantsPartial(b *testing.B) {
	rec := httptest.NewRecorder()
	base := router.NewBase(rec, requestWith(map[string]string{HeaderRequest: "true", HeaderRequestType: "partial"}))

	b.ReportAllocs()
	for b.Loop() {
		// Vary starts empty each round, as it does in a request.
		rec.Header().Del(router.HeaderVary)
		if !WantsPartial(base) {
			b.Fatal("WantsPartial() = false")
		}
	}
}

func BenchmarkRequestOf(b *testing.B) {
	req := requestWith(htmxHeaders)

	b.ReportAllocs()
	for b.Loop() {
		if RequestOf(req).TargetID() != "log" {
			b.Fatal("TargetID() != log")
		}
	}
}
