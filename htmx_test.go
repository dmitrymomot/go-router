package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hxDo sends a request whose headers the caller sets, which is how a test says
// that htmx made it.
func hxDo(h http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// htmxHeaders are the request headers of an htmx request that carries every
// field.
var htmxHeaders = map[string]string{
	HeaderHXRequest:               "true",
	HeaderHXBoosted:               "true",
	HeaderHXCurrentURL:            "https://example.com/chat",
	HeaderHXHistoryRestoreRequest: "true",
	HeaderHXPrompt:                "Ada",
	HeaderHXTarget:                "log",
	HeaderHXTrigger:               "send",
	HeaderHXTriggerName:           "message",
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
		Prompt:         "Ada",
		Target:         "log",
		Trigger:        "send",
		TriggerName:    "message",
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

// TestHTMXRequestSpelling proves that the constants are already in the form
// that net/http canonicalises to, so a lookup finds a header that a client
// spelled the way the htmx documentation does.
func TestHTMXRequestSpelling(t *testing.T) {
	for _, name := range []string{
		HeaderHXRequest, HeaderHXBoosted, HeaderHXCurrentURL,
		HeaderHXHistoryRestoreRequest, HeaderHXPrompt, HeaderHXTarget,
		HeaderHXTriggerName, HeaderHXTrigger, HeaderHXLocation,
		HeaderHXPushURL, HeaderHXRedirect, HeaderHXRefresh, HeaderHXReplaceURL,
		HeaderHXReswap, HeaderHXRetarget, HeaderHXReselect,
		HeaderHXTriggerAfterSettle, HeaderHXTriggerAfterSwap,
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
			"a boosted request gets the page",
			map[string]string{HeaderHXRequest: "true", HeaderHXBoosted: "true"},
			"page",
		},
		{
			"a history restore gets the page",
			map[string]string{HeaderHXRequest: "true", HeaderHXHistoryRestoreRequest: "true"},
			"page",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := hxDo(r, http.MethodGet, "/", tc.headers)
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
			if got := rec.Header().Values(HeaderVary); len(got) != 2 {
				t.Errorf("Vary = %v, want the two htmx headers", got)
			}
		})
	}
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
		c.Vary("hx-request") // the same name, in another case
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
			TriggerAfterSwap("swapped").
			TriggerAfterSettle("settled").
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
		{HeaderHXTriggerAfterSwap, "swapped"},
		{HeaderHXTriggerAfterSettle, "settled"},
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
			TriggerEventsAfterSwap(HXEvent{Name: "swapped", Detail: true}).
			TriggerEventsAfterSettle(HXEvent{Name: "settled", Detail: 1}).
			NoSwap()
	})

	rec := do(r, http.MethodGet, "/")
	for _, tc := range []struct{ header, want string }{
		{HeaderHXTrigger, `{"toast":"Gespeichert","count":{"rows":7},"plain":null}`},
		{HeaderHXTriggerAfterSwap, `{"swapped":true}`},
		{HeaderHXTriggerAfterSettle, `{"settled":1}`},
	} {
		if got := rec.Header().Get(tc.header); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.header, got, tc.want)
		}
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

// TestHXKeepsTheFirstFailure proves that a chain reports the failure that
// stopped it, and not one that a later call would have produced.
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

// TestHXBodyMethodsReportAFailedChain proves that every method which ends a
// chain reports the failure instead of writing a body.
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

// TestHXRejectsInvalidUTF8 proves that the JSON encoder, which refuses text
// that is not valid UTF-8, reports through the chain like any other failure.
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
