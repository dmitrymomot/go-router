package cookie

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

	"github.com/dmitrymomot/go-router"
)

var testKey = bytes.Repeat([]byte("k"), MinKeyLen)

func testCodec() *Codec { return NewCodec(testKey) }

// encode signs value for a day, the lifetime Set gives a session cookie.
func encode(cc *Codec, name string, value []byte) string {
	return cc.Encode(name, value, time.Now().Add(DefaultMaxAge))
}

func newBase(cookies ...*http.Cookie) *router.Base {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return router.NewBase(httptest.NewRecorder(), req)
}

func setCookies(t *testing.T, b *router.Base) []*http.Cookie {
	t.Helper()
	var out []*http.Cookie
	for _, line := range b.Response().Header()[router.HeaderSetCookie] {
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

func mustPanicWith(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Fatalf("no panic, want one that holds %q", want)
		}
		if msg, _ := r.(string); !strings.Contains(msg, want) {
			t.Errorf("panic = %v, want one that holds %q", r, want)
		}
	}()
	fn()
}

func TestNewCodecPanicsOnAShortKey(t *testing.T) {
	tests := []struct {
		name  string
		key   []byte
		panic bool
	}{
		{"no key at all", nil, true},
		{"an empty key", []byte{}, true},
		{"one byte short", bytes.Repeat([]byte("k"), MinKeyLen-1), true},
		{"the shortest key that works", bytes.Repeat([]byte("k"), MinKeyLen), false},
		{"a longer key", bytes.Repeat([]byte("k"), 64), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				switch {
				case r == nil && tc.panic:
					t.Error("NewCodec took a key that is too short")
				case r != nil && !tc.panic:
					t.Errorf("NewCodec panicked on a key it accepts: %v", r)
				case r != nil:
					if msg, _ := r.(string); !strings.HasPrefix(msg, "cookie: NewCodec") {
						t.Errorf("panic = %v, want one that starts with %q", r, "cookie: NewCodec")
					}
				}
			}()
			NewCodec(tc.key)
		})
	}
}

func TestNewCodecCopiesTheKey(t *testing.T) {
	key := bytes.Repeat([]byte("k"), MinKeyLen)
	cc := NewCodec(key)
	signed := encode(cc, "uid", []byte("7"))

	clear(key)

	if _, err := cc.Decode("uid", signed); err != nil {
		t.Errorf("the codec read the caller key after the caller zeroed it: %v", err)
	}
}

func TestNewCodecRotatesKeys(t *testing.T) {
	k1 := bytes.Repeat([]byte("1"), MinKeyLen)
	k2 := bytes.Repeat([]byte("2"), MinKeyLen)
	old := NewCodec(k1)
	rotated := NewCodec(k2, k1)

	got, err := rotated.Decode("uid", encode(old, "uid", []byte("alice")))
	if err != nil {
		t.Fatalf("a value the previous key signed: %v", err)
	}
	if string(got) != "alice" {
		t.Errorf("Decode = %q, want %q", got, "alice")
	}
	if _, err := old.Decode("uid", encode(rotated, "uid", []byte("alice"))); !errors.Is(err, ErrInvalid) {
		t.Errorf("the rotated codec signed with the previous key: %v", err)
	}
}

func TestNewCodecTriesEveryPreviousKey(t *testing.T) {
	k0 := bytes.Repeat([]byte("0"), MinKeyLen)
	k1 := bytes.Repeat([]byte("1"), MinKeyLen)
	k2 := bytes.Repeat([]byte("2"), MinKeyLen)
	cc := NewCodec(k2, k1, k0)

	got, err := cc.Decode("uid", encode(NewCodec(k0), "uid", []byte("alice")))
	if err != nil {
		t.Fatalf("a value the last previous key signed: %v", err)
	}
	if string(got) != "alice" {
		t.Errorf("Decode = %q, want %q", got, "alice")
	}

	unlisted := NewCodec(bytes.Repeat([]byte("x"), MinKeyLen))
	if _, err := cc.Decode("uid", encode(unlisted, "uid", []byte("alice"))); !errors.Is(err, ErrInvalid) {
		t.Errorf("a value an unlisted key signed: %v, want %v", err, ErrInvalid)
	}
}

func TestNewCodecPanicsOnAShortPreviousKey(t *testing.T) {
	short := bytes.Repeat([]byte("s"), MinKeyLen-1)
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
			mustPanicWith(t, tc.want, func() { NewCodec(testKey, tc.previous...) })
		})
	}
}

func TestNewCodecCopiesEveryKey(t *testing.T) {
	previous := bytes.Repeat([]byte("p"), MinKeyLen)
	signed := encode(NewCodec(previous), "uid", []byte("7"))
	cc := NewCodec(testKey, previous)

	clear(previous)

	if _, err := cc.Decode("uid", signed); err != nil {
		t.Errorf("the codec read the caller previous key after the caller zeroed it: %v", err)
	}
}

func TestZeroCodecPanics(t *testing.T) {
	const want = "cookie: use NewCodec to build a Codec"
	var nilCodec *Codec
	for name, cc := range map[string]*Codec{"the zero Codec": new(Codec), "a nil Codec": nilCodec} {
		t.Run(name, func(t *testing.T) {
			b := newBase()
			calls := map[string]func(){
				"Encode":   func() { cc.Encode("uid", []byte("alice"), time.Now().Add(time.Hour)) },
				"Decode":   func() { _, _ = cc.Decode("uid", "x.1.y") },
				"Set":      func() { _ = cc.Set(b, &http.Cookie{Name: "uid", Value: "alice"}) },
				"Get":      func() { _, _ = cc.Get(b, "uid") },
				"AddFlash": func() { _ = cc.AddFlash(b, Flash{Kind: "info", Message: "saved"}) },
				"Flashes":  func() { cc.Flashes(b) },
			}
			for method, call := range calls {
				t.Run(method, func(t *testing.T) { mustPanicWith(t, want, call) })
			}
		})
	}
}

func TestDecodeWithAnOldKeyStillChecksTheExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		previous := bytes.Repeat([]byte("p"), MinKeyLen)
		signed := NewCodec(previous).Encode("uid", []byte("alice"), time.Now().Add(time.Minute))
		cc := NewCodec(testKey, previous)

		time.Sleep(time.Minute)

		if _, err := cc.Decode("uid", signed); !errors.Is(err, ErrExpired) {
			t.Errorf("Decode = %v, want %v", err, ErrExpired)
		}
	})
}

func TestCodecRoundTripsAValue(t *testing.T) {
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
			got, err := cc.Decode("uid", encode(cc, "uid", tc.value))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !bytes.Equal(got, tc.value) {
				t.Errorf("Decode = %q, want %q", got, tc.value)
			}
		})
	}
}

func TestEncodeSignsTheGivenExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		expires := time.Now().Add(3 * time.Hour)
		if got, want := signedExpiryOf(t, testCodec().Encode("uid", []byte("alice"), expires)), expires.Unix(); got != want {
			t.Errorf("the expiry is %d, want %d", got, want)
		}
	})
}

func TestEncodeLeavesTheValueReadable(t *testing.T) {
	signed := encode(testCodec(), "uid", []byte("alice"))

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
	signed := encode(testCodec(), "uid", []byte{0x00, 0xff, ';', ',', ' ', '"', '\\'})

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
	signed := encode(cc, "uid", []byte("alice"))
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
			if _, err := cc.Decode("uid", tc.signed); !errors.Is(err, ErrInvalid) {
				t.Errorf("Decode = %v, want %v", err, ErrInvalid)
			}
		})
	}
}

func TestDecodeRejectsAnotherName(t *testing.T) {
	cc := testCodec()
	signed := encode(cc, "uid", []byte("alice"))

	if _, err := cc.Decode("admin", signed); !errors.Is(err, ErrInvalid) {
		t.Errorf("a value moved from one cookie into another: %v", err)
	}
}

func TestDecodeRejectsAnotherKey(t *testing.T) {
	signed := encode(testCodec(), "uid", []byte("alice"))
	other := NewCodec(bytes.Repeat([]byte("o"), MinKeyLen))

	if _, err := other.Decode("uid", signed); !errors.Is(err, ErrInvalid) {
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
		signed := cc.Encode("uid", []byte("alice"), time.Now().Add(time.Minute))

		if _, err := cc.Decode("uid", signed); err != nil {
			t.Fatalf("a fresh value: %v", err)
		}

		time.Sleep(time.Minute)

		if _, err := cc.Decode("uid", signed); !errors.Is(err, ErrExpired) {
			t.Errorf("Decode = %v, want %v", err, ErrExpired)
		}
	})
}

func TestDecodeChecksTheSignatureBeforeTheExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cc := testCodec()
		signed := cc.Encode("uid", []byte("alice"), time.Now().Add(time.Minute))
		time.Sleep(time.Minute)

		if _, err := cc.Decode("admin", signed); !errors.Is(err, ErrInvalid) {
			t.Errorf("Decode = %v, want %v", err, ErrInvalid)
		}
	})
}

func TestSetWritesTheSignedValue(t *testing.T) {
	cc := testCodec()
	b := newBase()
	if err := cc.Set(b, &http.Cookie{Name: "uid", Value: "alice", Path: "/", HttpOnly: true}); err != nil {
		t.Fatalf("Set: %v", err)
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

func TestSetLeavesTheCallerCookieAlone(t *testing.T) {
	c := &http.Cookie{Name: "uid", Value: "alice"}
	if err := testCodec().Set(newBase(), c); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if c.Value != "alice" {
		t.Errorf("the cookie of the caller now reads %q, want %q", c.Value, "alice")
	}
}

func TestSetRefusesACookieTooLarge(t *testing.T) {
	b := newBase()
	err := testCodec().Set(b, &http.Cookie{Name: "uid", Value: strings.Repeat("x", 5000)})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Set = %v, want %v", err, ErrTooLarge)
	}
	if lines := b.Response().Header()[router.HeaderSetCookie]; len(lines) != 0 {
		t.Errorf("Set wrote %q for a cookie it refused", lines)
	}
}

func TestSetDropsAnInvalidName(t *testing.T) {
	b := newBase()
	if err := testCodec().Set(b, &http.Cookie{Name: "bad name", Value: "v"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if lines := b.Response().Header()[router.HeaderSetCookie]; len(lines) != 0 {
		t.Errorf("Set wrote a cookie with an invalid name: %q", lines)
	}
}

func TestSetMatchesTheLifetimeOfTheCookie(t *testing.T) {
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
			func(now time.Time) int64 { return now.Add(DefaultMaxAge).Unix() },
		},
		{
			"a cookie that deletes another one",
			func(time.Time) *http.Cookie { return &http.Cookie{Name: "uid", MaxAge: -1} },
			func(now time.Time) int64 { return now.Add(DefaultMaxAge).Unix() },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				now := time.Now()
				b := newBase()
				if err := testCodec().Set(b, tc.cookie(now)); err != nil {
					t.Fatalf("Set: %v", err)
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

func TestGetReadsTheValue(t *testing.T) {
	cc := testCodec()
	b := newBase(&http.Cookie{Name: "uid", Value: encode(cc, "uid", []byte("alice"))})

	got, err := cc.Get(b, "uid")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "alice" {
		t.Errorf("Get = %q, want %q", got, "alice")
	}
}

func TestGetReportsTheFailure(t *testing.T) {
	cc := testCodec()
	tests := []struct {
		name    string
		cookies []*http.Cookie
		want    error
	}{
		{"a request without the cookie", nil, http.ErrNoCookie},
		{"a request with another cookie", []*http.Cookie{{Name: "other", Value: "x"}}, http.ErrNoCookie},
		{"a cookie that does not verify", []*http.Cookie{{Name: "uid", Value: "nonsense"}}, ErrInvalid},
		{
			"a cookie that another name signed",
			[]*http.Cookie{{Name: "uid", Value: encode(cc, "admin", []byte("alice"))}},
			ErrInvalid,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cc.Get(newBase(tc.cookies...), "uid")
			if !errors.Is(err, tc.want) {
				t.Errorf("Get = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestGetReportsAnExpiredCookie(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cc := testCodec()
		b := newBase(&http.Cookie{Name: "uid", Value: cc.Encode("uid", []byte("alice"), time.Now().Add(time.Minute))})

		time.Sleep(2 * time.Minute)

		if _, err := cc.Get(b, "uid"); !errors.Is(err, ErrExpired) {
			t.Errorf("Get = %v, want %v", err, ErrExpired)
		}
	})
}

func TestGetTakesTheCookieThatVerifies(t *testing.T) {
	cc := testCodec()
	b := newBase(
		&http.Cookie{Name: "uid", Value: "planted-by-a-neighbour"},
		&http.Cookie{Name: "uid", Value: encode(cc, "uid", []byte("alice"))},
	)

	got, err := cc.Get(b, "uid")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "alice" {
		t.Errorf("Get = %q, want %q", got, "alice")
	}
}

func TestNewCookieFeedsSet(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cc := testCodec()
		b := newBase()
		if err := cc.Set(b, b.NewCookie("s", "v", time.Hour)); err != nil {
			t.Fatalf("Set: %v", err)
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

		got, err := cc.Get(newBase(&http.Cookie{Name: "s", Value: c.Value}), "s")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got != "v" {
			t.Errorf("Get = %q, want %q", got, "v")
		}
	})
}

// appCtx holds the codec the way an app does: in its own context struct.
type appCtx struct {
	router.Base
	Cookies *Codec
	Tag     string
}

func signingRoutes(r *router.Router[*appCtx]) {
	r.POST("/signin", func(c *appCtx) error {
		if err := c.Cookies.Set(c, c.NewCookie("uid", "ann", time.Hour)); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
	r.GET("/me", func(c *appCtx) error {
		uid, err := c.Cookies.Get(c, "uid")
		if err != nil {
			return c.String(http.StatusUnauthorized, err.Error())
		}
		return c.String(http.StatusOK, uid)
	})
}

func testRouters(cc *Codec) []struct {
	name string
	r    *router.Router[*appCtx]
} {
	return []struct {
		name string
		r    *router.Router[*appCtx]
	}{
		{"New", router.New(func(http.ResponseWriter, *http.Request) *appCtx { return &appCtx{Cookies: cc} })},
		{"NewPooled", router.NewPooled(func() *appCtx { return &appCtx{Cookies: cc} }, func(c *appCtx) { c.Tag = "" })},
	}
}

func TestCodecSignsAcrossRequests(t *testing.T) {
	for _, tc := range testRouters(testCodec()) {
		t.Run(tc.name, func(t *testing.T) {
			signingRoutes(tc.r)

			signin := httptest.NewRecorder()
			tc.r.ServeHTTP(signin, httptest.NewRequest(http.MethodPost, "/signin", nil))
			if signin.Code != http.StatusNoContent {
				t.Fatalf("POST /signin = %d, want 204; body: %s", signin.Code, signin.Body)
			}
			line := signin.Header().Get(router.HeaderSetCookie)
			c, err := http.ParseSetCookie(line)
			if err != nil {
				t.Fatalf("the Set-Cookie does not parse: %q: %v", line, err)
			}

			for _, sub := range []struct {
				name  string
				value string
				code  int
				body  string
			}{
				{"the signed value", c.Value, http.StatusOK, "ann"},
				{"a tampered value", "YWRtaW4" + c.Value[strings.IndexByte(c.Value, '.'):], http.StatusUnauthorized, ErrInvalid.Error()},
			} {
				t.Run(sub.name, func(t *testing.T) {
					req := httptest.NewRequest(http.MethodGet, "/me", nil)
					req.AddCookie(&http.Cookie{Name: "uid", Value: sub.value})
					rec := httptest.NewRecorder()
					tc.r.ServeHTTP(rec, req)
					if rec.Code != sub.code || rec.Body.String() != sub.body {
						t.Errorf("GET /me = %d %q, want %d %q", rec.Code, rec.Body, sub.code, sub.body)
					}
				})
			}
		})
	}
}
