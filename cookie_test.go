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

	if bytes.Equal(cc.sign("ab", 0, []byte("cd")), cc.sign("a", 0, []byte("bcd"))) {
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
	b := cookieBase()
	b.SetSignedCookie(cc, &http.Cookie{Name: "uid", Value: "alice", Path: "/", HttpOnly: true})

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
	cookieBase().SetSignedCookie(testCodec(), c)

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

				b := cookieBase()
				b.SetSignedCookie(cc, tc.cookie(now))

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
	b := cookieBase(&http.Cookie{Name: "uid", Value: cc.Encode("uid", []byte("alice"))})

	got, err := b.SignedCookie(cc, "uid")
	if err != nil {
		t.Fatalf("SignedCookie: %v", err)
	}
	if string(got) != "alice" {
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
			_, err := cookieBase(tc.cookies...).SignedCookie(cc, "uid")
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
		b := cookieBase(&http.Cookie{Name: "uid", Value: cc.Encode("uid", []byte("alice"))})

		time.Sleep(2 * time.Minute)

		if _, err := b.SignedCookie(cc, "uid"); !errors.Is(err, ErrCookieExpired) {
			t.Errorf("SignedCookie = %v, want %v", err, ErrCookieExpired)
		}
	})
}

func TestSignedCookieTakesTheCookieThatVerifies(t *testing.T) {
	cc := testCodec()
	b := cookieBase(
		&http.Cookie{Name: "uid", Value: "planted-by-a-neighbour"},
		&http.Cookie{Name: "uid", Value: cc.Encode("uid", []byte("alice"))},
	)

	got, err := b.SignedCookie(cc, "uid")
	if err != nil {
		t.Fatalf("SignedCookie: %v", err)
	}
	if string(got) != "alice" {
		t.Errorf("SignedCookie = %q, want %q", got, "alice")
	}
}
