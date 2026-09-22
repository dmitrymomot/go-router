package router

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func hxDo(h http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var htmxHeaders = map[string]string{
	HeaderHXRequest:               "true",
	HeaderHXRequestType:           "partial",
	HeaderHXBoosted:               "true",
	HeaderHXCurrentURL:            "https://example.com/chat",
	HeaderHXHistoryRestoreRequest: "true",
	HeaderHXSource:                "button#send",
	HeaderHXTarget:                "ul#log",
}

func TestHTMXRequest(t *testing.T) {
	r := newTestRouter()
	var got HTMXRequest
	r.GET("/", func(c *tctx) error {
		got = c.HTMX()
		return c.NoContent(http.StatusOK)
	})

	hxDo(r, http.MethodGet, "/", htmxHeaders)

	want := HTMXRequest{
		CurrentURL:     "https://example.com/chat",
		RequestType:    "partial",
		Source:         "button#send",
		Target:         "ul#log",
		Request:        true,
		Boosted:        true,
		HistoryRestore: true,
	}
	if got != want {
		t.Errorf("HTMX() = %+v, want %+v", got, want)
	}
}

func TestHTMXRequestEmpty(t *testing.T) {
	r := newTestRouter()
	var got HTMXRequest
	var isHTMX, boosted bool
	r.GET("/", func(c *tctx) error {
		got, isHTMX, boosted = c.HTMX(), c.IsHTMX(), c.IsBoosted()
		return c.NoContent(http.StatusOK)
	})

	do(r, http.MethodGet, "/")

	if got != (HTMXRequest{}) {
		t.Errorf("HTMX() = %+v, want the zero value", got)
	}
	if isHTMX || boosted {
		t.Errorf("IsHTMX() = %v, IsBoosted() = %v, want false and false", isHTMX, boosted)
	}
}

func TestHTMXRequestTargetID(t *testing.T) {
	for _, tc := range htmxIDCases {
		if got := (HTMXRequest{Target: tc.in}).TargetID(); got != tc.want {
			t.Errorf("TargetID() of %q = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHTMXRequestSourceID(t *testing.T) {
	for _, tc := range htmxIDCases {
		if got := (HTMXRequest{Source: tc.in}).SourceID(); got != tc.want {
			t.Errorf("SourceID() of %q = %q, want %q", tc.in, got, tc.want)
		}
	}
}

var htmxIDCases = []struct{ in, want string }{
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

func TestIsHTMXIgnoresCase(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.Stringf(http.StatusOK, "%v %v", c.IsHTMX(), c.IsBoosted())
	})

	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"lower case", map[string]string{HeaderHXRequest: "true"}, "true false"},
		{"upper case", map[string]string{HeaderHXRequest: "TRUE"}, "true false"},
		{"boosted", map[string]string{HeaderHXRequest: "true", HeaderHXBoosted: "true"}, "true true"},
		{"a value that is not true", map[string]string{HeaderHXRequest: "1"}, "false false"},
		{"no header", nil, "false false"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hxDo(r, http.MethodGet, "/", tc.headers).Body.String(); got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHTMXRequestSpelling(t *testing.T) {
	for _, name := range []string{
		HeaderHXRequest, HeaderHXRequestType, HeaderHXBoosted, HeaderHXCurrentURL,
		HeaderHXHistoryRestoreRequest, HeaderHXSource, HeaderHXTarget,
		HeaderHXTrigger, HeaderHXLocation,
		HeaderHXPushURL, HeaderHXRedirect, HeaderHXRefresh, HeaderHXReplaceURL,
		HeaderHXReswap, HeaderHXRetarget, HeaderHXReselect,
	} {
		if got := http.CanonicalHeaderKey(name); got != name {
			t.Errorf("the constant %q is not canonical, want %q", name, got)
		}
	}

	h := http.Header{}
	h.Set("HX-Request", "true")
	if !hxTrue(h.Get(HeaderHXRequest)) {
		t.Error("a header that the client spelled HX-Request did not answer the constant")
	}
}

func TestHTMXPartial(t *testing.T) {
	r := newTestRouter()
	r.GET("/", HTMXPartial(
		func(c *tctx) error { return c.String(http.StatusOK, "partial") },
		func(c *tctx) error { return c.String(http.StatusOK, "page") },
	))

	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"a browser gets the page", nil, "page"},
		{"htmx gets the partial", map[string]string{HeaderHXRequest: "true"}, "partial"},
		{
			"a partial request gets the partial",
			map[string]string{HeaderHXRequest: "true", HeaderHXRequestType: "partial"},
			"partial",
		},
		{
			"a boosted request gets the page",
			map[string]string{HeaderHXRequest: "true", HeaderHXRequestType: "full", HeaderHXBoosted: "true"},
			"page",
		},
		{
			"a history restore gets the page",
			map[string]string{HeaderHXRequest: "true", HeaderHXRequestType: "full", HeaderHXHistoryRestoreRequest: "true"},
			"page",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := hxDo(r, http.MethodGet, "/", tc.headers)
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
			wantVary := []string{HeaderHXRequest, HeaderHXRequestType}
			if got := rec.Header().Values(HeaderVary); !slices.Equal(got, wantVary) {
				t.Errorf("Vary = %v, want %v", got, wantVary)
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
		{"a partial request", map[string]string{HeaderHXRequest: "true", HeaderHXRequestType: "partial"}, "", hxVary},
		{"a full request", map[string]string{HeaderHXRequest: "true", HeaderHXRequestType: "full"}, "", hxVary},
		{"an answer that varies on everything", map[string]string{HeaderHXRequest: "true"}, "*", []string{"*"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			if tc.vary != "" {
				rec.Header().Set(HeaderVary, tc.vary)
			}
			b := NewBase(rec, req)

			if got, want := b.WantsPartial(), HTMXWantsPartial(req); got != want {
				t.Errorf("WantsPartial() = %v, want %v, as HTMXWantsPartial answers", got, want)
			}
			b.WantsPartial()
			if got := rec.Header().Values(HeaderVary); !slices.Equal(got, tc.want) {
				t.Errorf("Vary after two calls = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRenderPartial(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.RenderPartial(http.StatusOK, comp("<li>card</li>"), comp("<h1>page</h1>"))
	})
	r.GET("/invalid", func(c *tctx) error {
		return c.RenderPartial(http.StatusUnprocessableEntity, comp("<form>partial</form>"), comp("<form>page</form>"))
	})
	r.GET("/broken", func(c *tctx) error {
		broken := ComponentFunc(func(context.Context, io.Writer) error { return errors.New("template failed") })
		return c.RenderPartial(http.StatusOK, broken, broken)
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
		{"a partial request gets the partial", map[string]string{HeaderHXRequest: "true", HeaderHXRequestType: "partial"}, partial},
		{
			"a full boosted request gets the page",
			map[string]string{HeaderHXRequest: "true", HeaderHXRequestType: "full", HeaderHXBoosted: "true"},
			page,
		},
		{
			"a full history restore gets the page",
			map[string]string{HeaderHXRequest: "true", HeaderHXRequestType: "full", HeaderHXHistoryRestoreRequest: "true"},
			page,
		},
		{"a request with no type gets the partial", map[string]string{HeaderHXRequest: "true"}, partial},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := hxDo(r, http.MethodGet, "/", tc.headers)
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
			if got := rec.Header().Get(HeaderContentType); got != MIMETextHTMLCharsetUTF8 {
				t.Errorf("Content-Type = %q, want %q", got, MIMETextHTMLCharsetUTF8)
			}
			if got := rec.Header().Values(HeaderVary); !slices.Equal(got, hxVary) {
				t.Errorf("Vary = %v, want %v", got, hxVary)
			}
		})
	}

	t.Run("both branches keep the status", func(t *testing.T) {
		for _, headers := range []map[string]string{nil, {HeaderHXRequest: "true"}} {
			rec := hxDo(r, http.MethodGet, "/invalid", headers)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422", rec.Code)
			}
		}
	})

	t.Run("HEAD writes no body", func(t *testing.T) {
		rec := hxDo(r, http.MethodHead, "/", map[string]string{HeaderHXRequest: "true"})
		if rec.Body.Len() != 0 {
			t.Errorf("body = %q, want an empty one", rec.Body.String())
		}
	})

	t.Run("a failing component answers 500", func(t *testing.T) {
		for _, headers := range []map[string]string{nil, {HeaderHXRequest: "true"}} {
			if code := hxDo(r, http.MethodGet, "/broken", headers).Code; code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", code)
			}
		}
	})
}

func TestHTMXPartialNeedsBothHandlers(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("HTMXPartial(nil, nil) did not panic")
		}
	}()
	_ = HTMXPartial[*tctx](nil, nil)
}

func TestVary(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		c.Vary(HeaderHXRequest, "")
		c.Vary("hx-request")
		c.Vary(HeaderAccept)
		return c.NoContent(http.StatusOK)
	})

	got := do(r, http.MethodGet, "/").Header().Values(HeaderVary)
	want := []string{HeaderHXRequest, HeaderAccept}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Vary = %v, want %v", got, want)
	}
}

func TestVarySeesAListThatOneHeaderHolds(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		c.SetHeader(HeaderVary, "Accept, HX-Request")
		c.Vary(HeaderHXRequest)
		return c.NoContent(http.StatusOK)
	})

	if got := do(r, http.MethodGet, "/").Header().Values(HeaderVary); len(got) != 1 {
		t.Errorf("Vary = %v, want the one header that the handler set", got)
	}
}

func TestHXHeaders(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.HX().
			PushURL("/rows/7").
			ReplaceURL("/rows/7?edit").
			Retarget("#row-7").
			Reselect("#row-7 td").
			Reswap(HXSwapOuterHTML).
			Refresh().
			Trigger("saved", "closed").
			Render(http.StatusOK, comp("<tr></tr>"))
	})

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "<tr></tr>" {
		t.Errorf("body = %q, want %q", got, "<tr></tr>")
	}
	for _, tc := range []struct{ header, want string }{
		{HeaderHXPushURL, "/rows/7"},
		{HeaderHXReplaceURL, "/rows/7?edit"},
		{HeaderHXRetarget, "#row-7"},
		{HeaderHXReselect, "#row-7 td"},
		{HeaderHXReswap, "outerHTML"},
		{HeaderHXRefresh, "true"},
		{HeaderHXTrigger, "saved, closed"},
	} {
		if got := rec.Header().Get(tc.header); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.header, got, tc.want)
		}
	}
}

func TestHXTriggerWithoutNamesWritesNothing(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.HX().Trigger().TriggerEvents().NoSwap()
	})

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if _, ok := rec.Header()[HeaderHXTrigger]; ok {
		t.Errorf("%s = %q, want no header at all", HeaderHXTrigger, rec.Header().Get(HeaderHXTrigger))
	}
}

func TestHXTriggerRejectsANameThatTheHeaderCannotCarry(t *testing.T) {
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
			r := newTestRouter()
			r.GET("/", func(c *tctx) error {
				err := c.HX().Trigger(tc.event).Err()
				if !errors.Is(err, tc.want) {
					t.Errorf("Err() = %v, want one that wraps %v", err, tc.want)
				}
				return c.HX().Trigger(tc.event).NoSwap()
			})

			rec := do(r, http.MethodGet, "/")
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if got := rec.Header().Get(HeaderHXTrigger); got != "" {
				t.Errorf("%s = %q, want no header", HeaderHXTrigger, got)
			}
		})
	}
}

func TestHXTriggerEvents(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.HX().
			TriggerEvents(
				HXEvent{Name: "toast", Detail: "Gespeichert"},
				HXEvent{Name: "count", Detail: map[string]int{"rows": 7}},
				HXEvent{Name: "plain"},
			).
			NoSwap()
	})

	got := do(r, http.MethodGet, "/").Header().Get(HeaderHXTrigger)
	want := `{"toast":"Gespeichert","count":{"rows":7},"plain":null}`
	if got != want {
		t.Errorf("%s = %q, want %q", HeaderHXTrigger, got, want)
	}
}

func TestHXTriggerEventsEscapeEveryCharacterOutsideASCII(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.HX().TriggerEvents(HXEvent{Name: "toast", Detail: "über 😀"}).NoSwap()
	})

	got := do(r, http.MethodGet, "/").Header().Get(HeaderHXTrigger)
	want := `{"toast":"\u00fcber \ud83d\ude00"}`
	if got != want {
		t.Errorf("%s = %q, want %q", HeaderHXTrigger, got, want)
	}
	if !isASCII(got) {
		t.Errorf("%s = %q, which is not ASCII", HeaderHXTrigger, got)
	}
}

func TestHXTriggerEventsNeedsANameForEveryEvent(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.HX().TriggerEvents(HXEvent{Detail: 1}).NoSwap()
	})

	if code := do(r, http.MethodGet, "/").Code; code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", code)
	}
}

func TestHXTriggerEventsReportsADetailThatCannotEncode(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.HX().TriggerEvents(HXEvent{Name: "bad", Detail: make(chan int)}).NoSwap()
	})

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get(HeaderHXTrigger); got != "" {
		t.Errorf("%s = %q, want no header", HeaderHXTrigger, got)
	}
}

func TestHXRejectsALineBreakInAHeader(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.HX().Retarget("#a\r\nHX-Redirect: /evil").Render(http.StatusOK, comp("x"))
	})

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get(HeaderHXRetarget); got != "" {
		t.Errorf("%s = %q, want no header", HeaderHXRetarget, got)
	}
}

func TestHXKeepsTheFirstFailure(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		err := c.HX().Retarget("a\nb").Trigger("also,bad").Err()
		if err == nil || !strings.Contains(err.Error(), HeaderHXRetarget) {
			t.Errorf("Err() = %v, want the failure of Retarget", err)
		}
		return c.NoContent(http.StatusOK)
	})

	do(r, http.MethodGet, "/")
}

func TestHXNoSwap(t *testing.T) {
	r := newTestRouter()
	r.POST("/messages", func(c *tctx) error {
		return c.HX().Trigger("message-sent").NoSwap()
	})

	rec := do(r, http.MethodPost, "/messages")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want an empty one", rec.Body.String())
	}
	if got := rec.Header().Get(HeaderHXTrigger); got != "message-sent" {
		t.Errorf("%s = %q, want %q", HeaderHXTrigger, got, "message-sent")
	}
}

func TestHXRedirect(t *testing.T) {
	r := newTestRouter()
	r.POST("/join", func(c *tctx) error { return c.HX().Redirect("/chat") })

	t.Run("htmx gets a client-side redirect", func(t *testing.T) {
		rec := hxDo(r, http.MethodPost, "/join", map[string]string{HeaderHXRequest: "true"})
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get(HeaderHXRedirect); got != "/chat" {
			t.Errorf("%s = %q, want %q", HeaderHXRedirect, got, "/chat")
		}
		if got := rec.Header().Get(HeaderLocation); got != "" {
			t.Errorf("%s = %q, want no header", HeaderLocation, got)
		}
	})

	t.Run("a browser gets a 303", func(t *testing.T) {
		rec := do(r, http.MethodPost, "/join")
		if rec.Code != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", rec.Code)
		}
		if got := rec.Header().Get(HeaderLocation); got != "/chat" {
			t.Errorf("%s = %q, want %q", HeaderLocation, got, "/chat")
		}
		if got := rec.Header().Get(HeaderHXRedirect); got != "" {
			t.Errorf("%s = %q, want no header", HeaderHXRedirect, got)
		}
	})
}

func TestHXRedirectCarriesTheFlash(t *testing.T) {
	r := newTestRouter()
	r.CookieCodec(testCodec())
	r.POST("/join", func(c *tctx) error {
		if err := c.AddFlash(Flash{Kind: "success", Message: "welcome"}); err != nil {
			return err
		}
		return c.HX().Redirect("/chat")
	})
	r.GET("/chat", func(c *tctx) error { return c.Stringf(http.StatusOK, "%v", c.Flashes()) })

	for _, tc := range []struct {
		name    string
		headers map[string]string
		status  int
	}{
		{"htmx gets a client-side redirect", map[string]string{HeaderHXRequest: "true"}, http.StatusOK},
		{"a browser gets a 303", nil, http.StatusSeeOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := hxDo(r, http.MethodPost, "/join", tc.headers)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			line := rec.Header().Get("Set-Cookie")
			if !strings.HasPrefix(line, FlashCookieName+"=") {
				t.Fatalf("the redirect carries Set-Cookie %q, want the flash cookie", line)
			}

			got := hxDo(r, http.MethodGet, "/chat", map[string]string{HeaderCookie: cookieHeader(t, line)})
			if want := "[{success welcome}]"; got.Body.String() != want {
				t.Errorf("the next page shows %q, want %q", got.Body, want)
			}
		})
	}
}

func TestHXLocation(t *testing.T) {
	r := newTestRouter()
	r.GET("/short", func(c *tctx) error { return c.HX().Location("/chat") })
	r.GET("/full", func(c *tctx) error {
		return c.HX().LocationWith(HXLocation{
			Path:   "/chat",
			Target: "#main",
			Swap:   HXSwapInnerHTML,
			Values: map[string]string{"room": "go"},
		})
	})
	r.GET("/select", func(c *tctx) error {
		return c.HX().LocationWith(HXLocation{Path: "/chat", Select: "#messages"})
	})
	r.GET("/path-only", func(c *tctx) error {
		return c.HX().LocationWith(HXLocation{Path: "/chat"})
	})
	r.GET("/no-path", func(c *tctx) error { return c.HX().LocationWith(HXLocation{}) })

	hx := map[string]string{HeaderHXRequest: "true"}

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
			rec := hxDo(r, http.MethodGet, tc.target, hx)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get(HeaderHXLocation); got != tc.want {
				t.Errorf("%s = %q, want %q", HeaderHXLocation, got, tc.want)
			}
		})
	}

	t.Run("a browser gets a 303", func(t *testing.T) {
		for _, target := range []string{"/short", "/full"} {
			rec := do(r, http.MethodGet, target)
			if rec.Code != http.StatusSeeOther {
				t.Errorf("%s: status = %d, want 303", target, rec.Code)
			}
			if got := rec.Header().Get(HeaderLocation); got != "/chat" {
				t.Errorf("%s: %s = %q, want %q", target, HeaderLocation, got, "/chat")
			}
		}
	})

	t.Run("a location needs a path", func(t *testing.T) {
		if code := hxDo(r, http.MethodGet, "/no-path", hx).Code; code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", code)
		}
	})
}

func TestHXBodyMethods(t *testing.T) {
	r := newTestRouter()
	r.GET("/render", func(c *tctx) error {
		return c.HX().Retarget("#a").Render(http.StatusOK, comp("<b>1</b>"))
	})
	r.GET("/stream", func(c *tctx) error {
		return c.HX().Retarget("#a").RenderStream(http.StatusAccepted, comp("<b>2</b>"))
	})
	r.GET("/html", func(c *tctx) error {
		return c.HX().Retarget("#a").HTML(http.StatusOK, "<b>3</b>")
	})
	r.GET("/string", func(c *tctx) error {
		return c.HX().Retarget("#a").String(http.StatusOK, "4")
	})
	r.GET("/json", func(c *tctx) error {
		return c.HX().Retarget("#a").JSON(http.StatusOK, map[string]int{"n": 5})
	})
	r.GET("/nocontent", func(c *tctx) error {
		return c.HX().Retarget("#a").NoContent(http.StatusResetContent)
	})

	tests := []struct {
		target string
		status int
		body   string
	}{
		{"/render", http.StatusOK, "<b>1</b>"},
		{"/stream", http.StatusAccepted, "<b>2</b>"},
		{"/html", http.StatusOK, "<b>3</b>"},
		{"/string", http.StatusOK, "4"},
		{"/json", http.StatusOK, `{"n":5}`},
		{"/nocontent", http.StatusResetContent, ""},
	}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			rec := do(r, http.MethodGet, tc.target)
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
			if got := rec.Body.String(); got != tc.body {
				t.Errorf("body = %q, want %q", got, tc.body)
			}
			if got := rec.Header().Get(HeaderHXRetarget); got != "#a" {
				t.Errorf("%s = %q, want %q", HeaderHXRetarget, got, "#a")
			}
		})
	}
}

func TestHXBodyMethodsReportAFailedChain(t *testing.T) {
	bad := func(c *tctx) HXResponse { return c.HX().Retarget("a\nb") }

	ends := map[string]func(c *tctx) error{
		"Render":       func(c *tctx) error { return bad(c).Render(http.StatusOK, comp("x")) },
		"RenderStream": func(c *tctx) error { return bad(c).RenderStream(http.StatusOK, comp("x")) },
		"HTML":         func(c *tctx) error { return bad(c).HTML(http.StatusOK, "x") },
		"String":       func(c *tctx) error { return bad(c).String(http.StatusOK, "x") },
		"JSON":         func(c *tctx) error { return bad(c).JSON(http.StatusOK, 1) },
		"NoContent":    func(c *tctx) error { return bad(c).NoContent(http.StatusOK) },
		"NoSwap":       func(c *tctx) error { return bad(c).NoSwap() },
		"Redirect":     func(c *tctx) error { return bad(c).Redirect("/x") },
		"Location":     func(c *tctx) error { return bad(c).Location("/x") },
		"LocationWith": func(c *tctx) error { return bad(c).LocationWith(HXLocation{Path: "/x"}) },
	}
	for name, end := range ends {
		t.Run(name, func(t *testing.T) {
			r := newTestRouter()
			r.GET("/", end)
			if code := hxDo(r, http.MethodGet, "/", map[string]string{HeaderHXRequest: "true"}).Code; code != http.StatusInternalServerError {
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

func BenchmarkHXResponse(b *testing.B) {
	r, w := benchRouter()
	r.GET("/rows/{id}", func(c *tctx) error {
		return c.HX().Retarget("#row").Reswap(HXSwapOuterHTML).Trigger("saved").NoSwap()
	})
	benchServe(b, r, w, "/rows/7")
}

func BenchmarkIsHTMX(b *testing.B) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderHXRequest, "true")
	base := NewBase(httptest.NewRecorder(), req)

	b.ReportAllocs()
	for b.Loop() {
		if !base.IsHTMX() {
			b.Fatal("IsHTMX() = false")
		}
	}
}

func BenchmarkWantsPartial(b *testing.B) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderHXRequest, "true")
	req.Header.Set(HeaderHXRequestType, "partial")

	rec := httptest.NewRecorder()
	base := NewBase(rec, req)

	b.ReportAllocs()
	for b.Loop() {
		// Vary starts empty each round, as it does in a request.
		rec.Header().Del(HeaderVary)
		if !base.WantsPartial() {
			b.Fatal("WantsPartial() = false")
		}
	}
}

func BenchmarkHTMX(b *testing.B) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for k, v := range htmxHeaders {
		req.Header.Set(k, v)
	}
	base := NewBase(httptest.NewRecorder(), req)

	b.ReportAllocs()
	for b.Loop() {
		if base.HTMX().TargetID() != "log" {
			b.Fatal("TargetID() != log")
		}
	}
}

func TestHXStopsAtTheFirstFailedHeader(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		return c.HX().Retarget("a\nb").PushURL("/x").NoSwap()
	})

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get(HeaderHXPushURL); got != "" {
		t.Errorf("%s = %q, want no header, because the chain had already failed", HeaderHXPushURL, got)
	}
}

func TestHXRedirectRejectsALineBreakInTheURL(t *testing.T) {
	r := newTestRouter()
	r.GET("/redirect", func(c *tctx) error { return c.HX().Redirect("/a\r\nHX-Refresh: true") })
	r.GET("/location", func(c *tctx) error { return c.HX().Location("/a\r\nHX-Refresh: true") })

	for _, target := range []string{"/redirect", "/location"} {
		t.Run(target, func(t *testing.T) {
			rec := hxDo(r, http.MethodGet, target, map[string]string{HeaderHXRequest: "true"})
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if got := rec.Header().Get(HeaderHXRefresh); got != "" {
				t.Errorf("%s = %q, want no header", HeaderHXRefresh, got)
			}
		})
	}
}

func TestHXRejectsInvalidUTF8(t *testing.T) {
	r := newTestRouter()
	r.GET("/trigger", func(c *tctx) error {
		return c.HX().TriggerEvents(HXEvent{Name: "\xff", Detail: 1}).NoSwap()
	})
	r.GET("/location", func(c *tctx) error {
		return c.HX().LocationWith(HXLocation{Path: "/chat", Values: map[string]string{"\xff": "x"}})
	})

	for _, target := range []string{"/trigger", "/location"} {
		t.Run(target, func(t *testing.T) {
			rec := hxDo(r, http.MethodGet, target, map[string]string{HeaderHXRequest: "true"})
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
		})
	}
}

func TestHXReportsAFailureOfADroppedLink(t *testing.T) {
	r := newTestRouter()
	r.GET("/", func(c *tctx) error {
		hx := c.HX()
		hx.Retarget("bad\nvalue")
		return hx.Render(http.StatusOK, comp("x"))
	})

	rec := do(r, http.MethodGet, "/")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get(HeaderHXRetarget); got != "" {
		t.Errorf("%s = %q, want no header", HeaderHXRetarget, got)
	}
}

func TestHXTriggerRejectsARepeatedName(t *testing.T) {
	ends := map[string]func(c *tctx) error{
		"Trigger": func(c *tctx) error {
			return c.HX().Trigger("saved", "saved").NoSwap()
		},
		"TriggerEvents": func(c *tctx) error {
			return c.HX().TriggerEvents(
				HXEvent{Name: "saved", Detail: 1},
				HXEvent{Name: "saved", Detail: 2},
			).NoSwap()
		},
	}
	for name, end := range ends {
		t.Run(name, func(t *testing.T) {
			r := newTestRouter()
			r.GET("/", end)

			rec := do(r, http.MethodGet, "/")
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if got := rec.Header().Get(HeaderHXTrigger); got != "" {
				t.Errorf("%s = %q, want no header", HeaderHXTrigger, got)
			}
		})
	}
}

func TestHTMXWantsPartial(t *testing.T) {
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
		{"htmx with no type", hx(HeaderHXRequest, "true"), true},
		{"partial", hx(HeaderHXRequest, "true", HeaderHXRequestType, "partial"), true},
		{"PARTIAL", hx(HeaderHXRequest, "true", HeaderHXRequestType, "PARTIAL"), true},
		{"full", hx(HeaderHXRequest, "true", HeaderHXRequestType, "full"), false},
		{"FULL", hx(HeaderHXRequest, "true", HeaderHXRequestType, "FULL"), false},
		{
			"full and boosted",
			hx(HeaderHXRequest, "true", HeaderHXRequestType, "full", HeaderHXBoosted, "true"),
			false,
		},
		{
			"partial and boosted",
			hx(HeaderHXRequest, "true", HeaderHXRequestType, "partial", HeaderHXBoosted, "true"),
			true,
		},
		{
			"full and a history restore",
			hx(HeaderHXRequest, "true", HeaderHXRequestType, "full", HeaderHXHistoryRestoreRequest, "true"),
			false,
		},
		{"htmx 2 boosted with no type", hx(HeaderHXRequest, "true", HeaderHXBoosted, "true"), true},
		{"a type without HX-Request", hx(HeaderHXRequestType, "partial"), false},
		{"an HX-Request that is not true", hx(HeaderHXRequest, "1"), false},
		{"a type htmx does not send", hx(HeaderHXRequest, "true", HeaderHXRequestType, "sideways"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			if got := HTMXWantsPartial(req); got != tc.want {
				t.Errorf("HTMXWantsPartial() = %v, want %v", got, tc.want)
			}
		})
	}
}
