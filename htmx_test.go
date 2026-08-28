package router

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// failHalfway writes part of a fragment and then fails, the way a template
// fails on a value that it cannot read.
func failHalfway(_ context.Context, w io.Writer) error {
	if _, err := io.WriteString(w, "<tr>partial"); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

// hxBase returns a Base whose request carries one header.
func hxBase(name, value string) *Base {
	req := httptest.NewRequest(http.MethodGet, "/orders", nil)
	if name != "" {
		req.Header.Set(name, value)
	}
	return NewBase(httptest.NewRecorder(), req)
}

func TestHXFlagAccessors(t *testing.T) {
	tests := []struct {
		name   string
		header string
		value  string
		read   func(*Base) bool
		want   bool
	}{
		{"an htmx request", HeaderHXRequest, "true", (*Base).HXRequest, true},
		{"a header in another case", HeaderHXRequest, "True", (*Base).HXRequest, true},
		{"a flag that is off", HeaderHXRequest, "false", (*Base).HXRequest, false},
		{"a request without the header", "", "", (*Base).HXRequest, false},
		{"a boosted request", HeaderHXBoosted, "true", (*Base).HXBoosted, true},
		{"a request that is not boosted", HeaderHXRequest, "true", (*Base).HXBoosted, false},
		{"a history restore", HeaderHXHistoryRestore, "true", (*Base).HXHistoryRestore, true},
		{"a request that restores nothing", HeaderHXRequest, "true", (*Base).HXHistoryRestore, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.read(hxBase(tc.header, tc.value)); got != tc.want {
				t.Errorf("the accessor answers %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHXStringAccessors(t *testing.T) {
	tests := []struct {
		name   string
		header string
		value  string
		read   func(*Base) string
	}{
		{"the target", HeaderHXTarget, "orders", (*Base).HXTarget},
		{"the trigger", HeaderHXTrigger, "search-form", (*Base).HXTrigger},
		{"the trigger name", HeaderHXTriggerName, "q", (*Base).HXTriggerName},
		{"the prompt", HeaderHXPrompt, "delete this", (*Base).HXPrompt},
		{"the current URL", HeaderHXCurrentURL, "https://example.com/orders", (*Base).HXCurrentURL},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.read(hxBase(tc.header, tc.value)); got != tc.value {
				t.Errorf("the accessor answers %q, want %q", got, tc.value)
			}
			if got := tc.read(hxBase("", "")); got != "" {
				t.Errorf("a request without the header answers %q, want empty", got)
			}
		})
	}
}

func TestHXResponseSetters(t *testing.T) {
	tests := []struct {
		name   string
		set    func(*Base)
		header string
		want   string
	}{
		{"a redirect", func(b *Base) { b.HXRedirect("/orders/7") }, HeaderHXRedirect, "/orders/7"},
		{"a location", func(b *Base) { b.HXLocation("/orders/7") }, HeaderHXLocation, "/orders/7"},
		{"a pushed URL", func(b *Base) { b.HXPushURL("/orders?page=2") }, HeaderHXPushURL, "/orders?page=2"},
		{"a replaced URL", func(b *Base) { b.HXReplaceURL("false") }, HeaderHXReplaceURL, "false"},
		{"another target", func(b *Base) { b.HXRetarget("#errors") }, HeaderHXRetarget, "#errors"},
		{"another swap", func(b *Base) { b.HXReswap("outerHTML") }, HeaderHXReswap, "outerHTML"},
		{
			"an event",
			func(b *Base) { b.HXTriggerEvent(`{"orderPlaced":{"id":7}}`) },
			HeaderHXTrigger, `{"orderPlaced":{"id":7}}`,
		},
		{"a refresh", (*Base).HXRefresh, HeaderHXRefresh, "true"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := hxBase("", "")
			tc.set(b)

			if got := b.Response().Header().Get(tc.header); got != tc.want {
				t.Errorf("%s = %q, want %q", tc.header, got, tc.want)
			}
			if b.Response().Committed {
				t.Error("the setter committed the response; a handler still has to write a body")
			}
		})
	}
}

// A header value that carries a line break ends the header and writes the rest
// of the response itself, so it never reaches the wire.
func TestHXSettersRejectALineBreak(t *testing.T) {
	setters := []struct {
		name   string
		set    func(*Base, string)
		header string
	}{
		{"HXRedirect", (*Base).HXRedirect, HeaderHXRedirect},
		{"HXLocation", (*Base).HXLocation, HeaderHXLocation},
		{"HXPushURL", (*Base).HXPushURL, HeaderHXPushURL},
		{"HXReplaceURL", (*Base).HXReplaceURL, HeaderHXReplaceURL},
		{"HXRetarget", (*Base).HXRetarget, HeaderHXRetarget},
		{"HXReswap", (*Base).HXReswap, HeaderHXReswap},
		{"HXTriggerEvent", (*Base).HXTriggerEvent, HeaderHXTrigger},
	}
	values := []struct {
		name  string
		value string
	}{
		{"a full line break", "/a\r\nX-Evil: 1"},
		{"a line feed", "/a\nX-Evil: 1"},
		{"a carriage return", "/a\rX-Evil: 1"},
	}
	for _, s := range setters {
		for _, v := range values {
			t.Run(s.name+" with "+v.name, func(t *testing.T) {
				logs := captureLogs(t)
				b := hxBase("", "")
				s.set(b, v.value)

				if got := b.Response().Header().Get(s.header); got != "" {
					t.Errorf("%s = %q, want none", s.header, got)
				}
				if len(logs.records) != 1 {
					t.Fatalf("logged %d records, want 1", len(logs.records))
				}
				if msg := logs.records[0].Message; !strings.Contains(msg, "line break") {
					t.Errorf("the record reads %q, want the reason for the drop", msg)
				}
			})
		}
	}
}

func TestRenderPartial(t *testing.T) {
	const (
		page     = "<html><table>rows</table></html>"
		fragment = "<tr>rows</tr>"
	)
	r := newTestRouter()
	r.GET("/orders", func(c *tctx) error {
		return c.RenderPartial(http.StatusOK, comp(page), comp(fragment))
	})

	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"a navigation reads the whole page", nil, page},
		{
			"an htmx request reads the fragment",
			map[string]string{HeaderHXRequest: "true"},
			fragment,
		},
		{
			"a boosted link replaces the body, so it reads the whole page",
			map[string]string{HeaderHXRequest: "true", HeaderHXBoosted: "true"},
			page,
		},
		{
			"a history restore reads the whole page",
			map[string]string{HeaderHXRequest: "true", HeaderHXHistoryRestore: "true"},
			page,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/orders", nil)
			for name, value := range tc.headers {
				req.Header.Set(name, value)
			}
			rec := doReq(r, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
			if got := rec.Header().Get(HeaderContentType); got != MIMETextHTMLCharsetUTF8 {
				t.Errorf("Content-Type = %q, want %q", got, MIMETextHTMLCharsetUTF8)
			}
			// Without the Vary header a shared cache hands the fragment to the
			// next browser that asks for the page.
			if got := rec.Header().Values(HeaderVary); !slices.Contains(got, varyHTMX) {
				t.Errorf("Vary = %q, want %q among them", got, varyHTMX)
			}
		})
	}
}

// The Vary of the route joins the one that the handler already set, rather than
// replacing it.
func TestRenderPartialKeepsAnExistingVary(t *testing.T) {
	r := newTestRouter()
	r.GET("/orders", func(c *tctx) error {
		c.SetHeader(HeaderVary, HeaderAccept)
		return c.RenderPartial(http.StatusOK, comp("page"), comp("rows"))
	})

	got := do(r, http.MethodGet, "/orders").Header().Values(HeaderVary)
	want := []string{HeaderAccept, varyHTMX}
	if !slices.Equal(got, want) {
		t.Errorf("Vary = %q, want %q", got, want)
	}
}

// A component that fails renders nothing, because RenderPartial goes through
// the buffered Render.
func TestRenderPartialWritesNoPartialBody(t *testing.T) {
	captureLogs(t)

	r := newTestRouter()
	r.GET("/orders", func(c *tctx) error {
		return c.RenderPartial(http.StatusOK, comp("page"), ComponentFunc(failHalfway))
	})

	req := httptest.NewRequest(http.MethodGet, "/orders", nil)
	req.Header.Set(HeaderHXRequest, "true")
	rec := doReq(r, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<tr>") {
		t.Errorf("body = %q, want no part of the fragment", rec.Body.String())
	}
}
