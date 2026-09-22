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

func flashBase(cookies ...*http.Cookie) *Base {
	return signedBase(testCodec(), cookies...)
}

func flashRequest(t *testing.T, b *Base) *Base {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range setCookies(t, b) {
		if c.MaxAge < 0 {
			continue
		}
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	next := NewBase(httptest.NewRecorder(), req)
	if cc := b.codec(); cc != nil {
		SetCookieCodecForTest(next, cc)
	}
	return next
}

func flashCookieOf(t *testing.T, b *Base) (*http.Cookie, bool) {
	t.Helper()
	for _, c := range setCookies(t, b) {
		if c.Name == FlashCookieName {
			return c, true
		}
	}
	return nil, false
}

func addFlashes(t *testing.T, b *Base, flashes ...Flash) {
	t.Helper()
	for _, f := range flashes {
		if err := b.AddFlash(f); err != nil {
			t.Fatalf("AddFlash(%+v): %v", f, err)
		}
	}
}

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
	post := flashBase()
	addFlashes(t, post, Flash{Kind: "success", Message: "saved"})

	get := flashRequest(t, post)
	wantFlashes(t, get.Flashes(), []Flash{{Kind: "success", Message: "saved"}})
}

func TestAddFlashKeepsTheOrderOfTheCalls(t *testing.T) {
	b := flashBase()
	addFlashes(t, b,
		Flash{Kind: "error", Message: "the name is taken"},
		Flash{Kind: "error", Message: "the password is short"},
		Flash{Kind: "info", Message: "try again"},
	)

	wantFlashes(t, flashRequest(t, b).Flashes(), []Flash{
		{Kind: "error", Message: "the name is taken"},
		{Kind: "error", Message: "the password is short"},
		{Kind: "info", Message: "try again"},
	})
}

func TestAddFlashWritesOneCookie(t *testing.T) {
	b := flashBase()
	addFlashes(t, b,
		Flash{Kind: "info", Message: "one"},
		Flash{Kind: "info", Message: "two"},
		Flash{Kind: "info", Message: "three"},
	)

	if got := len(b.Response().Header()["Set-Cookie"]); got != 1 {
		t.Errorf("the response carries %d Set-Cookie headers, want 1", got)
	}
}

func TestAddFlashAppendsToTheCookieOfTheRequest(t *testing.T) {
	first := flashBase()
	addFlashes(t, first, Flash{Kind: "info", Message: "one"})

	second := flashRequest(t, first)
	addFlashes(t, second, Flash{Kind: "info", Message: "two"})

	wantFlashes(t, flashRequest(t, second).Flashes(), []Flash{
		{Kind: "info", Message: "one"},
		{Kind: "info", Message: "two"},
	})
}

func TestFlashesClearsTheCookie(t *testing.T) {
	post := flashBase()
	addFlashes(t, post, Flash{Kind: "success", Message: "saved"})

	get := flashRequest(t, post)
	get.Flashes()

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
	post := flashBase()
	addFlashes(t, post, Flash{Kind: "success", Message: "saved"})

	get := flashRequest(t, post)
	wantFlashes(t, get.Flashes(), []Flash{{Kind: "success", Message: "saved"}})

	if got := get.Flashes(); got != nil {
		t.Errorf("the second call returned %+v, want nothing", got)
	}
	if got := len(get.Response().Header()["Set-Cookie"]); got != 1 {
		t.Errorf("the response carries %d Set-Cookie headers, want 1", got)
	}
}

func TestAddFlashAfterFlashesStartsAgain(t *testing.T) {
	post := flashBase()
	addFlashes(t, post, Flash{Kind: "info", Message: "old"})

	get := flashRequest(t, post)
	wantFlashes(t, get.Flashes(), []Flash{{Kind: "info", Message: "old"}})
	addFlashes(t, get, Flash{Kind: "info", Message: "new"})

	wantFlashes(t, flashRequest(t, get).Flashes(), []Flash{{Kind: "info", Message: "new"}})
}

func TestFlashesReturnsNothingWithoutACookie(t *testing.T) {
	b := flashBase()

	if got := b.Flashes(); got != nil {
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
			b := flashBase(&http.Cookie{Name: FlashCookieName, Value: tc.value})

			if got := b.Flashes(); got != nil {
				t.Errorf("Flashes returned %+v, want nothing", got)
			}
			if _, ok := flashCookieOf(t, b); !ok {
				t.Error("Flashes left the cookie in place, so the browser sends it again")
			}
		})
	}
}

func TestFlashesTakesTheCookieThatVerifies(t *testing.T) {
	post := flashBase()
	addFlashes(t, post, Flash{Kind: "success", Message: "saved"})
	signed, ok := flashCookieOf(t, post)
	if !ok {
		t.Fatal("AddFlash wrote no cookie")
	}

	get := flashBase(
		&http.Cookie{Name: FlashCookieName, Value: "planted-by-a-neighbour"},
		&http.Cookie{Name: FlashCookieName, Value: signed.Value},
	)

	wantFlashes(t, get.Flashes(), []Flash{{Kind: "success", Message: "saved"}})
}

func TestAddFlashTakesTheCookieThatVerifies(t *testing.T) {
	first := flashBase()
	addFlashes(t, first, Flash{Kind: "info", Message: "one"})
	signed, ok := flashCookieOf(t, first)
	if !ok {
		t.Fatal("AddFlash wrote no cookie")
	}

	second := flashBase(
		&http.Cookie{Name: FlashCookieName, Value: "planted-by-a-neighbour"},
		&http.Cookie{Name: FlashCookieName, Value: signed.Value},
	)
	addFlashes(t, second, Flash{Kind: "info", Message: "two"})

	wantFlashes(t, flashRequest(t, second).Flashes(), []Flash{
		{Kind: "info", Message: "one"},
		{Kind: "info", Message: "two"},
	})
}

func TestFlashesDropsAnExpiredCookie(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		post := flashBase()
		addFlashes(t, post, Flash{Kind: "info", Message: "saved"})
		get := flashRequest(t, post)

		time.Sleep(FlashMaxAge + time.Second)

		if got := get.Flashes(); got != nil {
			t.Errorf("Flashes returned %+v, want nothing", got)
		}
	})
}

func TestFlashesVariesOnTheCookie(t *testing.T) {
	post := flashBase()
	addFlashes(t, post, Flash{Kind: "info", Message: "saved"})

	get := flashRequest(t, post)
	get.Flashes()

	if got := get.Response().Header().Get(HeaderVary); got != HeaderCookie {
		t.Errorf("Vary = %q, want %q, or a shared cache hands one user the messages of another", got, HeaderCookie)
	}
}

func TestAddFlashWritesACookieAScriptCannotRead(t *testing.T) {
	b := flashBase()
	addFlashes(t, b, Flash{Kind: "info", Message: "saved"})

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

func TestAddFlashKeepsTheCookieForFlashMaxAge(t *testing.T) {
	b := flashBase()
	addFlashes(t, b, Flash{Kind: "info", Message: "saved"})

	c, ok := flashCookieOf(t, b)
	if !ok {
		t.Fatal("AddFlash wrote no cookie")
	}
	if want := int(FlashMaxAge / time.Second); c.MaxAge != want {
		t.Errorf("the flash cookie has MaxAge %d, want %d", c.MaxAge, want)
	}
}

func TestFlashesReadAfterAKeyRotation(t *testing.T) {
	previous := bytes.Repeat([]byte("p"), MinCookieKeyLen)
	post := signedBase(NewCookieCodec(previous))
	addFlashes(t, post, Flash{Kind: "success", Message: "saved"})

	get := flashRequest(t, post)
	SetCookieCodecForTest(get, NewCookieCodec(testKey, previous))
	wantFlashes(t, get.Flashes(), []Flash{{Kind: "success", Message: "saved"}})
}

func TestAddFlashMarksTheCookieSecureOverTLS(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderXForwardedProto, "https")
	b := NewBase(httptest.NewRecorder(), req)
	SetCookieCodecForTest(b, testCodec())
	addFlashes(t, b, Flash{Kind: "info", Message: "saved"})

	c, ok := flashCookieOf(t, b)
	if !ok {
		t.Fatal("AddFlash wrote no cookie")
	}
	if !c.Secure {
		t.Error("the flash cookie of an https request is not Secure")
	}
}

func TestAddFlashRefusesMoreThanACookieHolds(t *testing.T) {
	b := flashBase()
	addFlashes(t, b, Flash{Kind: "info", Message: "saved"})
	before, _ := flashCookieOf(t, b)

	err := b.AddFlash(Flash{Kind: "error", Message: strings.Repeat("x", MaxCookieSize)})
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
	wantFlashes(t, flashRequest(t, b).Flashes(), []Flash{{Kind: "info", Message: "saved"}})
}

func TestAddFlashReportsAMessageThatIsNotUTF8(t *testing.T) {
	if err := flashBase().AddFlash(Flash{Kind: "info", Message: "\xff"}); err == nil {
		t.Error("AddFlash took a message that is not UTF-8")
	}
}

func TestAddFlashMeasuresTheWholeCookie(t *testing.T) {
	b := flashBase()

	refused := false
	for i := range 200 {
		err := b.AddFlash(Flash{Kind: "info", Message: strings.Repeat("m", 32)})
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

func TestAddFlashWithoutACodecFails(t *testing.T) {
	b := cookieBase()

	if err := b.AddFlash(Flash{Kind: "info", Message: "saved"}); !errors.Is(err, ErrNoCookieCodec) {
		t.Errorf("AddFlash = %v, want %v", err, ErrNoCookieCodec)
	}
	if lines := b.Response().Header()["Set-Cookie"]; len(lines) != 0 {
		t.Errorf("AddFlash without a codec wrote %v", lines)
	}
	if got := b.Response().Header().Get(HeaderVary); got != "" {
		t.Errorf("AddFlash without a codec set Vary %q", got)
	}
}

func TestFlashesWithoutACodecLeavesTheCookie(t *testing.T) {
	post := flashBase()
	addFlashes(t, post, Flash{Kind: "success", Message: "saved"})
	signed, ok := flashCookieOf(t, post)
	if !ok {
		t.Fatal("AddFlash wrote no cookie")
	}

	get := cookieBase(&http.Cookie{Name: FlashCookieName, Value: signed.Value})
	if got := get.Flashes(); got != nil {
		t.Errorf("Flashes without a codec returned %+v, want nothing", got)
	}
	if h := get.Response().Header(); len(h) != 0 {
		t.Errorf("Flashes without a codec wrote the headers %v", h)
	}
}

func TestFlashesReadInTheSameRequestNeverLeaveTheServer(t *testing.T) {
	b := flashBase()
	addFlashes(t, b, Flash{Kind: "success", Message: "saved"})

	wantFlashes(t, b.Flashes(), []Flash{{Kind: "success", Message: "saved"}})

	h := b.Response().Header()
	if lines, ok := h["Set-Cookie"]; ok {
		t.Errorf("the response carries Set-Cookie %q, want no Set-Cookie key at all", lines)
	}
	if got := h.Get(HeaderVary); got != HeaderCookie {
		t.Errorf("Vary = %q, want %q", got, HeaderCookie)
	}
}

func TestFlashesKeepTheOtherCookiesOfTheResponse(t *testing.T) {
	b := flashBase()
	b.SetCookie(b.NewCookie("session", "abc", time.Hour))
	addFlashes(t, b, Flash{Kind: "success", Message: "saved"})
	b.Flashes()

	cookies := setCookies(t, b)
	if len(cookies) != 1 || cookies[0].Name != "session" || cookies[0].Value != "abc" {
		t.Errorf("the response sets %+v, want only the session cookie", cookies)
	}
}

func TestFlashesClearTheCookieTheRequestCarried(t *testing.T) {
	post := flashBase()
	addFlashes(t, post, Flash{Kind: "info", Message: "old"})

	get := flashRequest(t, post)
	addFlashes(t, get, Flash{Kind: "info", Message: "new"})
	wantFlashes(t, get.Flashes(), []Flash{
		{Kind: "info", Message: "old"},
		{Kind: "info", Message: "new"},
	})

	cookies := setCookies(t, get)
	if len(cookies) != 1 {
		t.Fatalf("the response sets %d cookies, want the one that clears the flash", len(cookies))
	}
	c := cookies[0]
	if c.Name != FlashCookieName || c.MaxAge >= 0 || c.Value != "" || c.Path != "/" {
		t.Errorf("the response sets %+v, want an expired, empty %s cookie on /", c, FlashCookieName)
	}
}

func TestAddFlashAfterASameRequestReadStartsAgain(t *testing.T) {
	b := flashBase()
	addFlashes(t, b, Flash{Kind: "info", Message: "a"})
	b.Flashes()
	addFlashes(t, b, Flash{Kind: "info", Message: "b"})

	wantFlashes(t, flashRequest(t, b).Flashes(), []Flash{{Kind: "info", Message: "b"}})
}
