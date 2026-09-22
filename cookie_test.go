package router

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

var testKey = bytes.Repeat([]byte("k"), MinCookieKeyLen)

func testCodec() *CookieCodec { return NewCookieCodec(testKey) }

func cookieBase(cookies ...*http.Cookie) *Base {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return NewBase(httptest.NewRecorder(), req)
}

func signedBase(cc *CookieCodec, cookies ...*http.Cookie) *Base {
	b := cookieBase(cookies...)
	SetCookieCodecForTest(b, cc)
	return b
}

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

func signedExpiryOf(t *testing.T, signed string) int64 {
	t.Helper()
	parts := strings.Split(signed, ".")
	if len(parts) != 3 {
		t.Fatalf("the signed value has %d fields, want 3: %q", len(parts), signed)
	}
	expiry, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		t.Fatalf("the expiry field does not parse: %q: %v", parts[1], err)
	}
	return expiry
}

func TestNewCookieCodecPanicsOnAShortKey(t *testing.T) {
	tests := []struct {
		name  string
		key   []byte
		panic bool
	}{
		{"no key at all", nil, true},
		{"an empty key", []byte{}, true},
		{"one byte short", bytes.Repeat([]byte("k"), MinCookieKeyLen-1), true},
		{"the shortest key that works", bytes.Repeat([]byte("k"), MinCookieKeyLen), false},
		{"a longer key", bytes.Repeat([]byte("k"), 64), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				switch r := recover(); {
				case r == nil && tc.panic:
					t.Error("NewCookieCodec took a key that is too short")
				case r != nil && !tc.panic:
					t.Errorf("NewCookieCodec panicked on a key it accepts: %v", r)
				}
			}()
			NewCookieCodec(tc.key)
		})
	}
}

func TestNewCookieCodecCopiesTheKey(t *testing.T) {
	key := bytes.Repeat([]byte("k"), MinCookieKeyLen)
	cc := NewCookieCodec(key)
	signed := cc.Encode("uid", []byte("7"))

	clear(key)

	if _, err := cc.Decode("uid", signed); err != nil {
		t.Errorf("the codec read the caller key after the caller zeroed it: %v", err)
	}
}

func TestNewCookieCodecRotatesKeys(t *testing.T) {
	k1 := bytes.Repeat([]byte("1"), MinCookieKeyLen)
	k2 := bytes.Repeat([]byte("2"), MinCookieKeyLen)
	old := NewCookieCodec(k1)
	rotated := NewCookieCodec(k2, k1)

	got, err := rotated.Decode("uid", old.Encode("uid", []byte("alice")))
	if err != nil {
		t.Fatalf("a value the previous key signed: %v", err)
	}
	if string(got) != "alice" {
		t.Errorf("Decode = %q, want %q", got, "alice")
	}
	if _, err := old.Decode("uid", rotated.Encode("uid", []byte("alice"))); !errors.Is(err, ErrCookieInvalid) {
		t.Errorf("the rotated codec signed with the previous key: %v", err)
	}
}

func TestNewCookieCodecTriesEveryPreviousKey(t *testing.T) {
	k0 := bytes.Repeat([]byte("0"), MinCookieKeyLen)
	k1 := bytes.Repeat([]byte("1"), MinCookieKeyLen)
	k2 := bytes.Repeat([]byte("2"), MinCookieKeyLen)
	cc := NewCookieCodec(k2, k1, k0)

	got, err := cc.Decode("uid", NewCookieCodec(k0).Encode("uid", []byte("alice")))
	if err != nil {
		t.Fatalf("a value the last previous key signed: %v", err)
	}
	if string(got) != "alice" {
		t.Errorf("Decode = %q, want %q", got, "alice")
	}

	unlisted := NewCookieCodec(bytes.Repeat([]byte("x"), MinCookieKeyLen))
	if _, err := cc.Decode("uid", unlisted.Encode("uid", []byte("alice"))); !errors.Is(err, ErrCookieInvalid) {
		t.Errorf("a value an unlisted key signed: %v, want %v", err, ErrCookieInvalid)
	}
}

func TestNewCookieCodecPanicsOnAShortPreviousKey(t *testing.T) {
	short := bytes.Repeat([]byte("s"), MinCookieKeyLen-1)
	tests := []struct {
		name     string
		previous [][]byte
		want     string
	}{
		{"no first previous key", [][]byte{nil}, "previous[0] has 0"},
		{"an empty first previous key", [][]byte{{}}, "previous[0] has 0"},
		{"a short first previous key", [][]byte{short}, "previous[0] has 31"},
		{"no second previous key", [][]byte{testKey, nil}, "previous[1] has 0"},
		{"an empty second previous key", [][]byte{testKey, {}}, "previous[1] has 0"},
		{"a short second previous key", [][]byte{testKey, short}, "previous[1] has 31"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("NewCookieCodec took a previous key that is too short")
				}
				if msg, _ := r.(string); !strings.Contains(msg, tc.want) {
					t.Errorf("panic = %v, want it to name %q", r, tc.want)
				}
			}()
			NewCookieCodec(testKey, tc.previous...)
		})
	}
}

func TestNewCookieCodecCopiesEveryKey(t *testing.T) {
	previous := bytes.Repeat([]byte("p"), MinCookieKeyLen)
	signed := NewCookieCodec(previous).Encode("uid", []byte("7"))
	cc := NewCookieCodec(testKey, previous)

	clear(previous)

	if _, err := cc.Decode("uid", signed); err != nil {
		t.Errorf("the codec read the caller previous key after the caller zeroed it: %v", err)
	}
}

func TestDecodeWithAnOldKeyStillChecksTheExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		previous := bytes.Repeat([]byte("p"), MinCookieKeyLen)
		old := NewCookieCodec(previous)
		old.MaxAge = time.Minute
		signed := old.Encode("uid", []byte("alice"))
		cc := NewCookieCodec(testKey, previous)

		time.Sleep(time.Minute)

		if _, err := cc.Decode("uid", signed); !errors.Is(err, ErrCookieExpired) {
			t.Errorf("Decode = %v, want %v", err, ErrCookieExpired)
		}
	})
}

func TestNewCookieCodecTakesTheDefaultLifetime(t *testing.T) {
	if got := testCodec().MaxAge; got != DefaultCookieMaxAge {
		t.Errorf("MaxAge = %v, want %v", got, DefaultCookieMaxAge)
	}
}

func TestCookieCodecRoundTripsAValue(t *testing.T) {
	tests := []struct {
		name  string
		value []byte
	}{
		{"an empty value", []byte{}},
		{"a nil value", nil},
		{"a word", []byte("7")},
		{"a value with a separator in it", []byte("a.b.c")},
		{"bytes that are not text", []byte{0x00, 0xff, 0x80, '\n', '"', ';', ','}},
		{"a long value", bytes.Repeat([]byte("x"), 1024)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cc := testCodec()
			got, err := cc.Decode("uid", cc.Encode("uid", tc.value))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !bytes.Equal(got, tc.value) {
				t.Errorf("Decode = %q, want %q", got, tc.value)
			}
		})
	}
}

func TestEncodeLeavesTheValueReadable(t *testing.T) {
	cc := testCodec()
	signed := cc.Encode("uid", []byte("alice"))

	encoded, _, _ := strings.Cut(signed, ".")
	value, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("the value field does not decode: %v", err)
	}
	if string(value) != "alice" {
		t.Errorf("the value field reads %q, want %q", value, "alice")
	}
}

func TestEncodeProducesACookieSafeValue(t *testing.T) {
	cc := testCodec()
	signed := cc.Encode("uid", []byte{0x00, 0xff, ';', ',', ' ', '"', '\\'})

	for i := 0; i < len(signed); i++ {
		switch ch := signed[i]; {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case ch == '-', ch == '_', ch == '.':
		default:
			t.Fatalf("the signed value carries %q, which does not belong in a cookie", ch)
		}
	}

	c := &http.Cookie{Name: "uid", Value: signed}
	if !strings.Contains(c.String(), signed) {
		t.Errorf("net/http rewrote the value: %q", c.String())
	}
}

func TestDecodeRejectsAValueThatDoesNotVerify(t *testing.T) {
	cc := testCodec()
	signed := cc.Encode("uid", []byte("alice"))
	value, expiry, sig := func() (string, string, string) {
		parts := strings.SplitN(signed, ".", 3)
		return parts[0], parts[1], parts[2]
	}()

	tests := []struct {
		name   string
		signed string
	}{
		{"an empty value", ""},
		{"no separator at all", value},
		{"one separator", value + "." + expiry},
		{"a value that is not base64", "!!!." + expiry + "." + sig},
		{"an expiry that is not a number", value + ".soon." + sig},
		{"a signature that is not base64", value + "." + expiry + ".!!!"},
		{"another value under the same signature", base64.RawURLEncoding.EncodeToString([]byte("bob")) + "." + expiry + "." + sig},
		{"a later expiry under the same signature", value + ".9999999999." + sig},
		{"a truncated signature", value + "." + expiry + "." + sig[:len(sig)-2]},
		{"a signature of the right length that is wrong", value + "." + expiry + "." + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0}, 32))},
		{"a field too many", signed + "." + sig},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cc.Decode("uid", tc.signed); !errors.Is(err, ErrCookieInvalid) {
				t.Errorf("Decode = %v, want %v", err, ErrCookieInvalid)
			}
		})
	}
}

func TestDecodeRejectsAnotherName(t *testing.T) {
	cc := testCodec()
	signed := cc.Encode("uid", []byte("alice"))

	if _, err := cc.Decode("admin", signed); !errors.Is(err, ErrCookieInvalid) {
		t.Errorf("a value moved from one cookie into another: %v", err)
	}
}

func TestDecodeRejectsAnotherKey(t *testing.T) {
	signed := testCodec().Encode("uid", []byte("alice"))
	other := NewCookieCodec(bytes.Repeat([]byte("o"), MinCookieKeyLen))

	if _, err := other.Decode("uid", signed); !errors.Is(err, ErrCookieInvalid) {
		t.Errorf("a value that another key signed verified: %v", err)
	}
}

func TestSignBindsTheLengthOfTheName(t *testing.T) {
	cc := testCodec()

	if bytes.Equal(cc.keys[0].sign("ab", 0, []byte("cd")), cc.keys[0].sign("a", 0, []byte("bcd"))) {
		t.Error("the signature reads a name and a value as one string of bytes")
	}
}

func TestDecodeReportsAnExpiredSignature(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cc := testCodec()
		cc.MaxAge = time.Minute
		signed := cc.Encode("uid", []byte("alice"))

		if _, err := cc.Decode("uid", signed); err != nil {
			t.Fatalf("a fresh value: %v", err)
		}

		time.Sleep(time.Minute)

		if _, err := cc.Decode("uid", signed); !errors.Is(err, ErrCookieExpired) {
			t.Errorf("Decode = %v, want %v", err, ErrCookieExpired)
		}
	})
}

func TestDecodeChecksTheSignatureBeforeTheExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cc := testCodec()
		cc.MaxAge = time.Minute
		signed := cc.Encode("uid", []byte("alice"))
		time.Sleep(time.Minute)

		if _, err := cc.Decode("admin", signed); !errors.Is(err, ErrCookieInvalid) {
			t.Errorf("Decode = %v, want %v", err, ErrCookieInvalid)
		}
	})
}

func TestEncodeTakesTheDefaultLifetimeForAZeroMaxAge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cc := testCodec()
		cc.MaxAge = 0

		want := time.Now().Add(DefaultCookieMaxAge).Unix()
		if got := signedExpiryOf(t, cc.Encode("uid", []byte("alice"))); got != want {
			t.Errorf("the expiry is %d, want %d", got, want)
		}
	})
}

func TestSetSignedCookieWritesTheSignedValue(t *testing.T) {
	cc := testCodec()
	b := signedBase(cc)
	if err := b.SetSignedCookie(&http.Cookie{Name: "uid", Value: "alice", Path: "/", HttpOnly: true}); err != nil {
		t.Fatalf("SetSignedCookie: %v", err)
	}

	cookies := setCookies(t, b)
	if len(cookies) != 1 {
		t.Fatalf("the response carries %d cookies, want 1", len(cookies))
	}
	got, err := cc.Decode("uid", cookies[0].Value)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if string(got) != "alice" {
		t.Errorf("the cookie carries %q, want %q", got, "alice")
	}
	if !cookies[0].HttpOnly || cookies[0].Path != "/" {
		t.Errorf("the attributes of the caller did not survive: %+v", cookies[0])
	}
}

func TestSetSignedCookieLeavesTheCallerCookieAlone(t *testing.T) {
	c := &http.Cookie{Name: "uid", Value: "alice"}
	if err := signedBase(testCodec()).SetSignedCookie(c); err != nil {
		t.Fatalf("SetSignedCookie: %v", err)
	}

	if c.Value != "alice" {
		t.Errorf("the cookie of the caller now reads %q, want %q", c.Value, "alice")
	}
}

func TestSetSignedCookieMatchesTheLifetimeOfTheCookie(t *testing.T) {
	tests := []struct {
		name   string
		cookie func(now time.Time) *http.Cookie
		want   func(now time.Time) int64
	}{
		{
			"a cookie that names a max age",
			func(time.Time) *http.Cookie { return &http.Cookie{Name: "uid", MaxAge: 3600} },
			func(now time.Time) int64 { return now.Add(time.Hour).Unix() },
		},
		{
			"a cookie that names a moment",
			func(now time.Time) *http.Cookie {
				return &http.Cookie{Name: "uid", Expires: now.Add(3 * time.Hour)}
			},
			func(now time.Time) int64 { return now.Add(3 * time.Hour).Unix() },
		},
		{
			"a cookie that lasts the browser session",
			func(time.Time) *http.Cookie { return &http.Cookie{Name: "uid"} },
			func(now time.Time) int64 { return now.Add(time.Minute).Unix() },
		},
		{
			"a cookie that deletes another one",
			func(time.Time) *http.Cookie { return &http.Cookie{Name: "uid", MaxAge: -1} },
			func(now time.Time) int64 { return now.Add(time.Minute).Unix() },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cc := testCodec()
				cc.MaxAge = time.Minute
				now := time.Now()

				b := signedBase(cc)
				if err := b.SetSignedCookie(tc.cookie(now)); err != nil {
					t.Fatalf("SetSignedCookie: %v", err)
				}

				cookies := setCookies(t, b)
				if len(cookies) != 1 {
					t.Fatalf("the response carries %d cookies, want 1", len(cookies))
				}
				if got, want := signedExpiryOf(t, cookies[0].Value), tc.want(now); got != want {
					t.Errorf("the signature expires at %d, want %d", got, want)
				}
			})
		})
	}
}

func TestSignedCookieReadsTheValue(t *testing.T) {
	cc := testCodec()
	b := signedBase(cc, &http.Cookie{Name: "uid", Value: cc.Encode("uid", []byte("alice"))})

	got, err := b.SignedCookie("uid")
	if err != nil {
		t.Fatalf("SignedCookie: %v", err)
	}
	if got != "alice" {
		t.Errorf("SignedCookie = %q, want %q", got, "alice")
	}
}

func TestSignedCookieReportsTheFailure(t *testing.T) {
	cc := testCodec()
	tests := []struct {
		name    string
		cookies []*http.Cookie
		want    error
	}{
		{"a request without the cookie", nil, http.ErrNoCookie},
		{"a request with another cookie", []*http.Cookie{{Name: "other", Value: "x"}}, http.ErrNoCookie},
		{"a cookie that does not verify", []*http.Cookie{{Name: "uid", Value: "nonsense"}}, ErrCookieInvalid},
		{
			"a cookie that another name signed",
			[]*http.Cookie{{Name: "uid", Value: cc.Encode("admin", []byte("alice"))}},
			ErrCookieInvalid,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := signedBase(cc, tc.cookies...).SignedCookie("uid")
			if !errors.Is(err, tc.want) {
				t.Errorf("SignedCookie = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSignedCookieReportsAnExpiredCookie(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cc := testCodec()
		cc.MaxAge = time.Minute
		b := signedBase(cc, &http.Cookie{Name: "uid", Value: cc.Encode("uid", []byte("alice"))})

		time.Sleep(2 * time.Minute)

		if _, err := b.SignedCookie("uid"); !errors.Is(err, ErrCookieExpired) {
			t.Errorf("SignedCookie = %v, want %v", err, ErrCookieExpired)
		}
	})
}

func TestSignedCookieTakesTheCookieThatVerifies(t *testing.T) {
	cc := testCodec()
	b := signedBase(cc,
		&http.Cookie{Name: "uid", Value: "planted-by-a-neighbour"},
		&http.Cookie{Name: "uid", Value: cc.Encode("uid", []byte("alice"))},
	)

	got, err := b.SignedCookie("uid")
	if err != nil {
		t.Fatalf("SignedCookie: %v", err)
	}
	if got != "alice" {
		t.Errorf("SignedCookie = %q, want %q", got, "alice")
	}
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

func TestNewCookieFeedsSetSignedCookie(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cc := testCodec()
		b := signedBase(cc)
		if err := b.SetSignedCookie(b.NewCookie("s", "v", time.Hour)); err != nil {
			t.Fatalf("SetSignedCookie: %v", err)
		}

		cookies := setCookies(t, b)
		if len(cookies) != 1 {
			t.Fatalf("the response carries %d cookies, want 1", len(cookies))
		}
		c := cookies[0]
		if c.Path != "/" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 3600 {
			t.Errorf("the defaults of NewCookie did not survive: %+v", c)
		}
		if got, want := signedExpiryOf(t, c.Value), time.Now().Add(time.Hour).Unix(); got != want {
			t.Errorf("the signature expires at %d, want %d", got, want)
		}

		got, err := signedBase(cc, &http.Cookie{Name: "s", Value: c.Value}).SignedCookie("s")
		if err != nil {
			t.Fatalf("SignedCookie: %v", err)
		}
		if got != "v" {
			t.Errorf("SignedCookie = %q, want %q", got, "v")
		}
	})
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

func TestSetSignedCookieWithoutACodecFails(t *testing.T) {
	b := cookieBase()

	if err := b.SetSignedCookie(b.NewCookie("uid", "alice", time.Hour)); !errors.Is(err, ErrNoCookieCodec) {
		t.Errorf("SetSignedCookie = %v, want %v", err, ErrNoCookieCodec)
	}
	if lines := b.Response().Header()["Set-Cookie"]; len(lines) != 0 {
		t.Errorf("SetSignedCookie without a codec wrote %q", lines)
	}
}

func TestSignedCookieWithoutACodecFails(t *testing.T) {
	tests := []struct {
		name    string
		cookies []*http.Cookie
	}{
		{"a request without the cookie", nil},
		{"a request with the cookie", []*http.Cookie{{Name: "uid", Value: testCodec().Encode("uid", []byte("alice"))}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cookieBase(tc.cookies...).SignedCookie("uid"); !errors.Is(err, ErrNoCookieCodec) {
				t.Errorf("SignedCookie = %v, want %v", err, ErrNoCookieCodec)
			}
		})
	}
}

func TestSetCookieCodecForTestPanicsOnNil(t *testing.T) {
	tests := []struct {
		name string
		b    *Base
		cc   *CookieCodec
		want string
	}{
		{"no Base", nil, testCodec(), "needs a Base"},
		{"no codec", cookieBase(), nil, "needs a codec"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				msg, _ := recover().(string)
				if !strings.Contains(msg, tc.want) {
					t.Errorf("panic = %q, want one that holds %q", msg, tc.want)
				}
			}()
			SetCookieCodecForTest(tc.b, tc.cc)
		})
	}
}

func TestSetCookieCodecForTestLeavesOtherBasesAlone(t *testing.T) {
	first := cookieBase()
	SetCookieCodecForTest(first, testCodec())
	second := cookieBase()
	other := NewCookieCodec(bytes.Repeat([]byte("o"), MinCookieKeyLen))
	SetCookieCodecForTest(second, other)

	if err := cookieBase().SetSignedCookie(&http.Cookie{Name: "uid"}); !errors.Is(err, ErrNoCookieCodec) {
		t.Errorf("a fresh Base took the codec of another: %v", err)
	}
	if first.codec() == second.codec() || second.codec() != other {
		t.Error("two Bases share one codec after each got its own")
	}
	if defaultRouterOpts.codec != nil {
		t.Error("SetCookieCodecForTest wrote the shared default options")
	}
}

func TestCookieCodecOf(t *testing.T) {
	cc := testCodec()
	withCodec := newTestRouter()
	withCodec.CookieCodec(cc)
	var scope *Router[*tctx]
	withCodec.Route("/admin", func(g *Router[*tctx]) { scope = g })

	tests := []struct {
		name string
		h    http.Handler
		want *CookieCodec
	}{
		{"a router with a codec", withCodec, cc},
		{"a scope of that router", scope, cc},
		{"a router without a codec", newTestRouter(), nil},
		{"a handler that is not a router", http.NotFoundHandler(), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CookieCodecOf(tc.h); got != tc.want {
				t.Errorf("CookieCodecOf = %p, want %p", got, tc.want)
			}
		})
	}
}
