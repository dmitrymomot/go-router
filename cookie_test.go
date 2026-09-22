package router

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func setCookies(t *testing.T, b *Base) []*http.Cookie {
	t.Helper()
	var out []*http.Cookie
	for _, line := range b.Response().Header()["Set-Cookie"] {
		c, err := http.ParseSetCookie(line)
		if err != nil {
			t.Fatalf("the response carries a Set-Cookie that does not parse: %q: %v", line, err)
		}
		out = append(out, c)
	}
	return out
}

// cookieHeader turns a Set-Cookie line into the name=value pair a browser
// sends back, without the attributes.
func cookieHeader(t *testing.T, line string) string {
	t.Helper()
	c, err := http.ParseSetCookie(line)
	if err != nil {
		t.Fatalf("the Set-Cookie does not parse: %q: %v", line, err)
	}
	return (&http.Cookie{Name: c.Name, Value: c.Value}).String()
}

func TestCookieReportsTheValue(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"a request without the cookie", "", ""},
		{"an empty cookie", "session=", ""},
		{"a cookie with a value", "session=abc", "abc"},
		{"the name sent twice", "session=first; session=second", "first"},
		{"another cookie", "other=x", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				req.Header.Set("Cookie", tc.header)
			}
			b := NewBase(httptest.NewRecorder(), req)
			if got := b.Cookie("session"); got != tc.want {
				t.Errorf("Cookie(session) = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewCookieTakesTheRouterDefaults(t *testing.T) {
	c := newBase("/").NewCookie("session", "abc", time.Hour)

	want := http.Cookie{
		Name:     "session",
		Value:    "abc",
		Path:     "/",
		MaxAge:   3600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if c.Name != want.Name || c.Value != want.Value || c.Path != want.Path || c.Domain != "" ||
		c.MaxAge != want.MaxAge || !c.HttpOnly || c.SameSite != want.SameSite || c.Secure {
		t.Errorf("NewCookie = %+v, want %+v", *c, want)
	}
}

func TestNewCookieIsSecureOverHTTPS(t *testing.T) {
	tests := []struct {
		name   string
		target string
		proto  string
		want   bool
	}{
		{"a plain request", "/", "", false},
		{"a TLS request", "https://example.com/", "", true},
		{"a proxy that says https", "/", "https", true},
		{"a proxy that says http", "/", "http", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			if tc.proto != "" {
				req.Header.Set(HeaderXForwardedProto, tc.proto)
			}
			c := NewBase(httptest.NewRecorder(), req).NewCookie("session", "abc", time.Hour)
			if c.Secure != tc.want {
				t.Errorf("Secure = %v, want %v", c.Secure, tc.want)
			}
		})
	}
}

func TestNewCookieRoundsTheMaxAge(t *testing.T) {
	tests := []struct {
		name   string
		maxAge time.Duration
		want   int
		line   string
	}{
		{"a session cookie", 0, 0, ""},
		{"twelve hours", 12 * time.Hour, 43200, "Max-Age=43200"},
		{"a second and a half", 1500 * time.Millisecond, 2, "Max-Age=2"},
		{"a nanosecond", time.Nanosecond, 1, "Max-Age=1"},
		{"a nanosecond ago", -time.Nanosecond, -1, "Max-Age=0"},
		{"an hour ago", -time.Hour, -1, "Max-Age=0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newBase("/").NewCookie("session", "abc", tc.maxAge)
			if c.MaxAge != tc.want {
				t.Errorf("MaxAge = %d, want %d", c.MaxAge, tc.want)
			}
			line := c.String()
			if tc.line == "" {
				if strings.Contains(line, "Max-Age") {
					t.Errorf("a session cookie carries a Max-Age: %q", line)
				}
				return
			}
			if !strings.Contains(line, tc.line) {
				t.Errorf("Set-Cookie = %q, want it to carry %q", line, tc.line)
			}
		})
	}
}

func TestClearCookieExpiresTheCookie(t *testing.T) {
	for _, https := range []bool{false, true} {
		t.Run("https="+strconv.FormatBool(https), func(t *testing.T) {
			target := "/"
			if https {
				target = "https://example.com/"
			}
			b := newBase(target)
			b.ClearCookie("session")

			lines := b.Response().Header()["Set-Cookie"]
			if len(lines) != 1 {
				t.Fatalf("ClearCookie wrote %d lines, want 1: %q", len(lines), lines)
			}
			if !strings.Contains(lines[0], "Max-Age=0") {
				t.Errorf("Set-Cookie = %q, want it to carry Max-Age=0", lines[0])
			}
			c := setCookies(t, b)[0]
			if c.Name != "session" || c.Value != "" || c.MaxAge != -1 || c.Path != "/" ||
				!c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure != https {
				t.Errorf("ClearCookie wrote %+v", c)
			}
		})
	}
}

func TestSetCookieDropsAnInvalidName(t *testing.T) {
	b := newBase("/")
	b.SetCookie(b.NewCookie("bad name", "v", 0))

	if lines := b.Response().Header()["Set-Cookie"]; len(lines) != 0 {
		t.Errorf("SetCookie wrote a cookie with an invalid name: %q", lines)
	}
}
