package router

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// flashRequest returns a Base whose request carries the cookies that the
// response of b set, the way the next request from the browser does. A cookie
// that b expired stays behind, because the browser dropped it.
func flashRequest(t *testing.T, b *Base) *Base {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range setCookies(t, b) {
		if c.MaxAge < 0 {
			continue
		}
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	return NewBase(httptest.NewRecorder(), req)
}

// flashCookieOf returns the flash cookie that the response of b carries, and
// reports whether it found one.
func flashCookieOf(t *testing.T, b *Base) (*http.Cookie, bool) {
	t.Helper()
	for _, c := range setCookies(t, b) {
		if c.Name == FlashCookieName {
			return c, true
		}
	}
	return nil, false
}

// addFlashes adds every message and fails on the first that does not fit.
func addFlashes(t *testing.T, b *Base, cc *CookieCodec, flashes ...Flash) {
	t.Helper()
	for _, f := range flashes {
		if err := b.AddFlash(cc, f); err != nil {
			t.Fatalf("AddFlash(%+v): %v", f, err)
		}
	}
}

// wantFlashes fails unless got holds exactly want.
func wantFlashes(t *testing.T, got, want []Flash) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("Flashes returned %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d is %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestFlashesCrossARedirect(t *testing.T) {
	cc := testCodec()
	post := cookieBase()
	addFlashes(t, post, cc, Flash{Kind: "success", Message: "saved"})

	get := flashRequest(t, post)
	wantFlashes(t, get.Flashes(cc), []Flash{{Kind: "success", Message: "saved"}})
}

func TestAddFlashKeepsTheOrderOfTheCalls(t *testing.T) {
	cc := testCodec()
	b := cookieBase()
	addFlashes(t, b, cc,
		Flash{Kind: "error", Message: "the name is taken"},
		Flash{Kind: "error", Message: "the password is short"},
		Flash{Kind: "info", Message: "try again"},
	)

	wantFlashes(t, flashRequest(t, b).Flashes(cc), []Flash{
		{Kind: "error", Message: "the name is taken"},
		{Kind: "error", Message: "the password is short"},
		{Kind: "info", Message: "try again"},
	})
}

// Three calls leave one cookie, not three, because a browser keeps the last
// header of a name and the rest is weight.
func TestAddFlashWritesOneCookie(t *testing.T) {
	cc := testCodec()
	b := cookieBase()
	addFlashes(t, b, cc,
		Flash{Kind: "info", Message: "one"},
		Flash{Kind: "info", Message: "two"},
		Flash{Kind: "info", Message: "three"},
	)

	if got := len(b.Response().Header()["Set-Cookie"]); got != 1 {
		t.Errorf("the response carries %d Set-Cookie headers, want 1", got)
	}
}

// A second request that adds a message keeps the one that the first left.
func TestAddFlashAppendsToTheCookieOfTheRequest(t *testing.T) {
	cc := testCodec()
	first := cookieBase()
	addFlashes(t, first, cc, Flash{Kind: "info", Message: "one"})

	second := flashRequest(t, first)
	addFlashes(t, second, cc, Flash{Kind: "info", Message: "two"})

	wantFlashes(t, flashRequest(t, second).Flashes(cc), []Flash{
		{Kind: "info", Message: "one"},
		{Kind: "info", Message: "two"},
	})
}

func TestFlashesClearsTheCookie(t *testing.T) {
	cc := testCodec()
	post := cookieBase()
	addFlashes(t, post, cc, Flash{Kind: "success", Message: "saved"})

	get := flashRequest(t, post)
	get.Flashes(cc)

	c, ok := flashCookieOf(t, get)
	if !ok {
		t.Fatal("Flashes wrote no cookie, so the browser keeps the messages")
	}
	if c.MaxAge >= 0 {
		t.Errorf("the clearing cookie has MaxAge %d, want a cookie that expired", c.MaxAge)
	}
	if c.Value != "" {
		t.Errorf("the clearing cookie carries %q, want an empty value", c.Value)
	}
	if c.Path != "/" {
		t.Errorf("the clearing cookie has path %q, want %q, or the browser keeps the old one", c.Path, "/")
	}
}

func TestFlashesIsSafeToCallTwice(t *testing.T) {
	cc := testCodec()
	post := cookieBase()
	addFlashes(t, post, cc, Flash{Kind: "success", Message: "saved"})

	get := flashRequest(t, post)
	wantFlashes(t, get.Flashes(cc), []Flash{{Kind: "success", Message: "saved"}})

	if got := get.Flashes(cc); got != nil {
		t.Errorf("the second call returned %+v, want nothing", got)
	}
	if got := len(get.Response().Header()["Set-Cookie"]); got != 1 {
		t.Errorf("the response carries %d Set-Cookie headers, want 1", got)
	}
}

// A handler that reads the messages and then adds one starts a new list, and
// hands the browser the message it just added and nothing it already showed.
func TestAddFlashAfterFlashesStartsAgain(t *testing.T) {
	cc := testCodec()
	post := cookieBase()
	addFlashes(t, post, cc, Flash{Kind: "info", Message: "old"})

	get := flashRequest(t, post)
	wantFlashes(t, get.Flashes(cc), []Flash{{Kind: "info", Message: "old"}})
	addFlashes(t, get, cc, Flash{Kind: "info", Message: "new"})

	wantFlashes(t, flashRequest(t, get).Flashes(cc), []Flash{{Kind: "info", Message: "new"}})
}

func TestFlashesReturnsNothingWithoutACookie(t *testing.T) {
	b := cookieBase()

	if got := b.Flashes(testCodec()); got != nil {
		t.Errorf("Flashes returned %+v, want nothing", got)
	}
	if lines := b.Response().Header()["Set-Cookie"]; len(lines) != 0 {
		t.Errorf("Flashes wrote %v, and there was nothing to clear", lines)
	}
}

func TestFlashesDropsACookieItCannotTrust(t *testing.T) {
	cc := testCodec()
	other := NewCookieCodec(bytes.Repeat([]byte("o"), MinCookieKeyLen))

	tests := []struct {
		name  string
		value string
	}{
		{"a value that is not signed", `[{"kind":"error","message":"you are an admin"}]`},
		{"a value that another key signed", other.Encode(FlashCookieName, []byte(`[{"kind":"error","message":"x"}]`))},
		{"a value that another cookie signed", cc.Encode("other", []byte(`[{"kind":"error","message":"x"}]`))},
		{"a value that does not hold a list", cc.Encode(FlashCookieName, []byte(`{"kind":"error"}`))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := cookieBase(&http.Cookie{Name: FlashCookieName, Value: tc.value})

			if got := b.Flashes(cc); got != nil {
				t.Errorf("Flashes returned %+v, want nothing", got)
			}
			if _, ok := flashCookieOf(t, b); !ok {
				t.Error("Flashes left the cookie in place, so the browser sends it again")
			}
		})
	}
}

func TestFlashesDropsAnExpiredCookie(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cc := testCodec()
		post := cookieBase()
		addFlashes(t, post, cc, Flash{Kind: "info", Message: "saved"})
		get := flashRequest(t, post)

		time.Sleep(FlashMaxAge + time.Second)

		if got := get.Flashes(cc); got != nil {
			t.Errorf("Flashes returned %+v, want nothing", got)
		}
	})
}

func TestFlashesVariesOnTheCookie(t *testing.T) {
	cc := testCodec()
	post := cookieBase()
	addFlashes(t, post, cc, Flash{Kind: "info", Message: "saved"})

	get := flashRequest(t, post)
	get.Flashes(cc)

	if got := get.Response().Header().Get(HeaderVary); got != HeaderCookie {
		t.Errorf("Vary = %q, want %q, or a shared cache hands one user the messages of another", got, HeaderCookie)
	}
}

func TestAddFlashWritesACookieAScriptCannotRead(t *testing.T) {
	b := cookieBase()
	addFlashes(t, b, testCodec(), Flash{Kind: "info", Message: "saved"})

	c, ok := flashCookieOf(t, b)
	if !ok {
		t.Fatal("AddFlash wrote no cookie")
	}
	if !c.HttpOnly {
		t.Error("the flash cookie is not HttpOnly")
	}
	if c.Path != "/" {
		t.Errorf("the flash cookie has path %q, want %q", c.Path, "/")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("the flash cookie has SameSite %v, want Lax", c.SameSite)
	}
	if c.Secure {
		t.Error("the flash cookie of a plain request is Secure, which the browser drops")
	}
}

func TestAddFlashMarksTheCookieSecureOverTLS(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderXForwardedProto, "https")
	b := NewBase(httptest.NewRecorder(), req)
	addFlashes(t, b, testCodec(), Flash{Kind: "info", Message: "saved"})

	c, ok := flashCookieOf(t, b)
	if !ok {
		t.Fatal("AddFlash wrote no cookie")
	}
	if !c.Secure {
		t.Error("the flash cookie of an https request is not Secure")
	}
}

func TestAddFlashRefusesMoreThanACookieHolds(t *testing.T) {
	cc := testCodec()
	b := cookieBase()
	addFlashes(t, b, cc, Flash{Kind: "info", Message: "saved"})
	before, _ := flashCookieOf(t, b)

	err := b.AddFlash(cc, Flash{Kind: "error", Message: strings.Repeat("x", MaxCookieSize)})
	if !errors.Is(err, ErrFlashTooLarge) {
		t.Fatalf("AddFlash = %v, want %v", err, ErrFlashTooLarge)
	}

	after, ok := flashCookieOf(t, b)
	if !ok {
		t.Fatal("the refused message took the cookie with it")
	}
	if after.Value != before.Value {
		t.Error("the refused message changed the cookie that the response already carried")
	}
	wantFlashes(t, flashRequest(t, b).Flashes(cc), []Flash{{Kind: "info", Message: "saved"}})
}

func TestAddFlashReportsAMessageThatIsNotUTF8(t *testing.T) {
	if err := cookieBase().AddFlash(testCodec(), Flash{Kind: "info", Message: "\xff"}); err == nil {
		t.Error("AddFlash took a message that is not UTF-8")
	}
}

// The cookie stays inside the size that a browser guarantees, which is what
// makes the limit worth checking against the whole header line.
func TestAddFlashMeasuresTheWholeCookie(t *testing.T) {
	cc := testCodec()
	b := cookieBase()

	refused := false
	for i := range 200 {
		err := b.AddFlash(cc, Flash{Kind: "info", Message: strings.Repeat("m", 32)})
		if err == nil {
			continue
		}
		if !errors.Is(err, ErrFlashTooLarge) {
			t.Fatalf("AddFlash: %v", err)
		}
		if i == 0 {
			t.Fatal("the first message did not fit")
		}
		refused = true
		break
	}
	if !refused {
		t.Fatal("200 messages fit in one cookie, so nothing measured the limit")
	}

	c, ok := flashCookieOf(t, b)
	if !ok {
		t.Fatal("AddFlash wrote no cookie")
	}
	if got := len(c.String()); got > MaxCookieSize {
		t.Errorf("the cookie is %d bytes, want at most %d", got, MaxCookieSize)
	}
}
