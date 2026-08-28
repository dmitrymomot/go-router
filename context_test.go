package router

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newBase returns a Base bound to a GET of the target, the way the router binds
// one before it calls a handler.
func newBase(target string) *Base {
	return NewBase(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
}

func TestIsTLSReadsTheConnection(t *testing.T) {
	if b := newBase("/"); b.IsTLS() {
		t.Error("IsTLS reported TLS on a plain request")
	}
	if b := newBase("https://example.com/"); !b.IsTLS() {
		t.Error("IsTLS reported no TLS on a TLS request")
	}
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
		// The connection is the truth, whatever the header claims.
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

func TestQueryOKTellsAbsentFromEmpty(t *testing.T) {
	b := newBase("/search?q=go&empty=&multi=a&multi=b")

	tests := []struct {
		name  string
		param string
		want  string
		found bool
	}{
		{"a value", "q", "go", true},
		{"an empty value", "empty", "", true},
		{"the first of several", "multi", "a", true},
		{"a parameter the query has not", "page", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := b.QueryOK(tc.param)
			if got != tc.want || ok != tc.found {
				t.Errorf("QueryOK(%q) = %q/%v, want %q/%v", tc.param, got, ok, tc.want, tc.found)
			}
		})
	}
}

func TestQueryHelpersReadTheSameParse(t *testing.T) {
	b := newBase("/search?q=go&empty=")

	if got := b.Query("q"); got != "go" {
		t.Errorf("Query(%q) = %q", "q", got)
	}
	if got := b.QueryDefault("empty", "all"); got != "all" {
		t.Errorf("QueryDefault of an empty value = %q, want the default", got)
	}
	if got := b.QueryDefault("q", "all"); got != "go" {
		t.Errorf("QueryDefault(%q) = %q", "q", got)
	}

	// The parse is cached, which is what makes QueryValues the map that the
	// other two read. A caller that changes it changes their answer, which is
	// why the doc comment forbids it.
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

	// A middleware that rewrites the URL changes what the handler reads.
	b.SetRequest(httptest.NewRequest(http.MethodGet, "/search?q=rust", nil))
	if got := b.Query("q"); got != "rust" {
		t.Errorf("Query(%q) = %q, want the value of the new request", "q", got)
	}
}

func TestInitDropsTheParsedQuery(t *testing.T) {
	// A pooled context must not answer with the query of the request before it.
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
	r.StrictBind(true)
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
	if !opts.strictBind {
		t.Error("strictBind = false, want true")
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
		{"the strict binding setting", func(r *Router[*tctx]) { r.StrictBind(true) }},
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
