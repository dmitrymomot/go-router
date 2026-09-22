package cookie

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/htmx"
)

var flashCodec = testCodec()

// flashRequest builds the next request a browser sends after the response of
// b, with the cookies that response set and did not clear.
func flashRequest(t *testing.T, b *router.Base) *router.Base {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range setCookies(t, b) {
		if c.MaxAge < 0 {
			continue
		}
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	return router.NewBase(httptest.NewRecorder(), req)
}

func flashCookieOf(t *testing.T, b *router.Base) (*http.Cookie, bool) {
	t.Helper()
	for _, c := range setCookies(t, b) {
		if c.Name == FlashName {
			return c, true
		}
	}
	return nil, false
}

func addFlashes(t *testing.T, cc *Codec, b *router.Base, flashes ...Flash) {
	t.Helper()
	for _, f := range flashes {
		if err := cc.AddFlash(b, f); err != nil {
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
	post := newBase()
	addFlashes(t, flashCodec, post, Flash{Kind: "success", Message: "saved"})

	get := flashRequest(t, post)
	wantFlashes(t, flashCodec.Flashes(get), []Flash{{Kind: "success", Message: "saved"}})
}

func TestAddFlashKeepsTheOrderOfTheCalls(t *testing.T) {
	b := newBase()
	addFlashes(t, flashCodec, b,
		Flash{Kind: "error", Message: "the name is taken"},
		Flash{Kind: "error", Message: "the password is short"},
		Flash{Kind: "info", Message: "try again"},
	)

	wantFlashes(t, flashCodec.Flashes(flashRequest(t, b)), []Flash{
		{Kind: "error", Message: "the name is taken"},
		{Kind: "error", Message: "the password is short"},
		{Kind: "info", Message: "try again"},
	})
}

func TestAddFlashWritesOneCookie(t *testing.T) {
	b := newBase()
	addFlashes(t, flashCodec, b,
		Flash{Kind: "info", Message: "one"},
		Flash{Kind: "info", Message: "two"},
		Flash{Kind: "info", Message: "three"},
	)

	if got := len(b.Response().Header()["Set-Cookie"]); got != 1 {
		t.Errorf("the response carries %d Set-Cookie headers, want 1", got)
	}
}

func TestAddFlashAppendsToTheCookieOfTheRequest(t *testing.T) {
	first := newBase()
	addFlashes(t, flashCodec, first, Flash{Kind: "info", Message: "one"})

	second := flashRequest(t, first)
	addFlashes(t, flashCodec, second, Flash{Kind: "info", Message: "two"})

	wantFlashes(t, flashCodec.Flashes(flashRequest(t, second)), []Flash{
		{Kind: "info", Message: "one"},
		{Kind: "info", Message: "two"},
	})
}

func TestFlashesClearsTheCookie(t *testing.T) {
	post := newBase()
	addFlashes(t, flashCodec, post, Flash{Kind: "success", Message: "saved"})

	get := flashRequest(t, post)
	flashCodec.Flashes(get)

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
	post := newBase()
	addFlashes(t, flashCodec, post, Flash{Kind: "success", Message: "saved"})

	get := flashRequest(t, post)
	wantFlashes(t, flashCodec.Flashes(get), []Flash{{Kind: "success", Message: "saved"}})

	if got := flashCodec.Flashes(get); got != nil {
		t.Errorf("the second call returned %+v, want nothing", got)
	}
	if got := len(get.Response().Header()["Set-Cookie"]); got != 1 {
		t.Errorf("the response carries %d Set-Cookie headers, want 1", got)
	}
}

func TestAddFlashAfterFlashesStartsAgain(t *testing.T) {
	post := newBase()
	addFlashes(t, flashCodec, post, Flash{Kind: "info", Message: "old"})

	get := flashRequest(t, post)
	wantFlashes(t, flashCodec.Flashes(get), []Flash{{Kind: "info", Message: "old"}})
	addFlashes(t, flashCodec, get, Flash{Kind: "info", Message: "new"})

	wantFlashes(t, flashCodec.Flashes(flashRequest(t, get)), []Flash{{Kind: "info", Message: "new"}})
}

func TestFlashesReturnsNothingWithoutACookie(t *testing.T) {
	b := newBase()

	if got := flashCodec.Flashes(b); got != nil {
		t.Errorf("Flashes returned %+v, want nothing", got)
	}
	if lines := b.Response().Header()["Set-Cookie"]; len(lines) != 0 {
		t.Errorf("Flashes wrote %v, and there was nothing to clear", lines)
	}
}

func TestFlashesDropsACookieItCannotTrust(t *testing.T) {
	cc := flashCodec
	other := NewCodec(bytes.Repeat([]byte("o"), MinKeyLen))

	tests := []struct {
		name  string
		value string
	}{
		{"a value that is not signed", `[{"kind":"error","message":"you are an admin"}]`},
		{"a value that another key signed", encode(other, FlashName, []byte(`[{"kind":"error","message":"x"}]`))},
		{"a value that another cookie signed", encode(cc, "other", []byte(`[{"kind":"error","message":"x"}]`))},
		{"a value that does not hold a list", encode(cc, FlashName, []byte(`{"kind":"error"}`))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newBase(&http.Cookie{Name: FlashName, Value: tc.value})

			if got := flashCodec.Flashes(b); got != nil {
				t.Errorf("Flashes returned %+v, want nothing", got)
			}
			if _, ok := flashCookieOf(t, b); !ok {
				t.Error("Flashes left the cookie in place, so the browser sends it again")
			}
		})
	}
}

func TestFlashesTakesTheCookieThatVerifies(t *testing.T) {
	post := newBase()
	addFlashes(t, flashCodec, post, Flash{Kind: "success", Message: "saved"})
	signed, ok := flashCookieOf(t, post)
	if !ok {
		t.Fatal("AddFlash wrote no cookie")
	}

	get := newBase(
		&http.Cookie{Name: FlashName, Value: "planted-by-a-neighbour"},
		&http.Cookie{Name: FlashName, Value: signed.Value},
	)

	wantFlashes(t, flashCodec.Flashes(get), []Flash{{Kind: "success", Message: "saved"}})
}

func TestAddFlashTakesTheCookieThatVerifies(t *testing.T) {
	first := newBase()
	addFlashes(t, flashCodec, first, Flash{Kind: "info", Message: "one"})
	signed, ok := flashCookieOf(t, first)
	if !ok {
		t.Fatal("AddFlash wrote no cookie")
	}

	second := newBase(
		&http.Cookie{Name: FlashName, Value: "planted-by-a-neighbour"},
		&http.Cookie{Name: FlashName, Value: signed.Value},
	)
	addFlashes(t, flashCodec, second, Flash{Kind: "info", Message: "two"})

	wantFlashes(t, flashCodec.Flashes(flashRequest(t, second)), []Flash{
		{Kind: "info", Message: "one"},
		{Kind: "info", Message: "two"},
	})
}

func TestFlashesDropsAnExpiredCookie(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		post := newBase()
		addFlashes(t, flashCodec, post, Flash{Kind: "info", Message: "saved"})
		get := flashRequest(t, post)

		time.Sleep(FlashMaxAge + time.Second)

		if got := flashCodec.Flashes(get); got != nil {
			t.Errorf("Flashes returned %+v, want nothing", got)
		}
	})
}

func TestFlashesVariesOnTheCookie(t *testing.T) {
	post := newBase()
	addFlashes(t, flashCodec, post, Flash{Kind: "info", Message: "saved"})

	get := flashRequest(t, post)
	flashCodec.Flashes(get)

	if got := get.Response().Header().Get(router.HeaderVary); got != router.HeaderCookie {
		t.Errorf("Vary = %q, want %q, or a shared cache hands one user the messages of another", got, router.HeaderCookie)
	}
}

func TestAddFlashWritesACookieAScriptCannotRead(t *testing.T) {
	b := newBase()
	addFlashes(t, flashCodec, b, Flash{Kind: "info", Message: "saved"})

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
	b := newBase()
	addFlashes(t, flashCodec, b, Flash{Kind: "info", Message: "saved"})

	c, ok := flashCookieOf(t, b)
	if !ok {
		t.Fatal("AddFlash wrote no cookie")
	}
	if want := int(FlashMaxAge / time.Second); c.MaxAge != want {
		t.Errorf("the flash cookie has MaxAge %d, want %d", c.MaxAge, want)
	}
}

func TestFlashesReadAfterAKeyRotation(t *testing.T) {
	previous := bytes.Repeat([]byte("p"), MinKeyLen)
	post := newBase()
	addFlashes(t, NewCodec(previous), post, Flash{Kind: "success", Message: "saved"})

	get := flashRequest(t, post)
	wantFlashes(t, NewCodec(testKey, previous).Flashes(get), []Flash{{Kind: "success", Message: "saved"}})
}

func TestAddFlashMarksTheCookieSecureOverTLS(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(router.HeaderXForwardedProto, "https")
	b := router.NewBase(httptest.NewRecorder(), req)
	addFlashes(t, flashCodec, b, Flash{Kind: "info", Message: "saved"})

	c, ok := flashCookieOf(t, b)
	if !ok {
		t.Fatal("AddFlash wrote no cookie")
	}
	if !c.Secure {
		t.Error("the flash cookie of an https request is not Secure")
	}
}

func TestAddFlashRefusesMoreThanACookieHolds(t *testing.T) {
	b := newBase()
	addFlashes(t, flashCodec, b, Flash{Kind: "info", Message: "saved"})
	before, _ := flashCookieOf(t, b)

	err := flashCodec.AddFlash(b, Flash{Kind: "error", Message: strings.Repeat("x", MaxSize)})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("AddFlash = %v, want %v", err, ErrTooLarge)
	}

	after, ok := flashCookieOf(t, b)
	if !ok {
		t.Fatal("the refused message took the cookie with it")
	}
	if after.Value != before.Value {
		t.Error("the refused message changed the cookie that the response already carried")
	}
	wantFlashes(t, flashCodec.Flashes(flashRequest(t, b)), []Flash{{Kind: "info", Message: "saved"}})
}

func TestAddFlashReportsAMessageThatIsNotUTF8(t *testing.T) {
	if err := flashCodec.AddFlash(newBase(), Flash{Kind: "info", Message: "\xff"}); err == nil {
		t.Error("AddFlash took a message that is not UTF-8")
	}
}

func TestAddFlashMeasuresTheWholeCookie(t *testing.T) {
	b := newBase()

	refused := false
	for i := range 200 {
		err := flashCodec.AddFlash(b, Flash{Kind: "info", Message: strings.Repeat("m", 32)})
		if err == nil {
			continue
		}
		if !errors.Is(err, ErrTooLarge) {
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
	if got := len(c.String()); got > MaxSize {
		t.Errorf("the cookie is %d bytes, want at most %d", got, MaxSize)
	}
}

func TestFlashesReadInTheSameRequestNeverLeaveTheServer(t *testing.T) {
	b := newBase()
	addFlashes(t, flashCodec, b, Flash{Kind: "success", Message: "saved"})

	wantFlashes(t, flashCodec.Flashes(b), []Flash{{Kind: "success", Message: "saved"}})

	h := b.Response().Header()
	if lines, ok := h["Set-Cookie"]; ok {
		t.Errorf("the response carries Set-Cookie %q, want no Set-Cookie key at all", lines)
	}
	if got := h.Get(router.HeaderVary); got != router.HeaderCookie {
		t.Errorf("Vary = %q, want %q", got, router.HeaderCookie)
	}
}

func TestFlashesKeepTheOtherCookiesOfTheResponse(t *testing.T) {
	b := newBase()
	b.SetCookie(b.NewCookie("session", "abc", time.Hour))
	addFlashes(t, flashCodec, b, Flash{Kind: "success", Message: "saved"})
	flashCodec.Flashes(b)

	cookies := setCookies(t, b)
	if len(cookies) != 1 || cookies[0].Name != "session" || cookies[0].Value != "abc" {
		t.Errorf("the response sets %+v, want only the session cookie", cookies)
	}
}

func TestFlashesClearTheCookieTheRequestCarried(t *testing.T) {
	post := newBase()
	addFlashes(t, flashCodec, post, Flash{Kind: "info", Message: "old"})

	get := flashRequest(t, post)
	addFlashes(t, flashCodec, get, Flash{Kind: "info", Message: "new"})
	wantFlashes(t, flashCodec.Flashes(get), []Flash{
		{Kind: "info", Message: "old"},
		{Kind: "info", Message: "new"},
	})

	cookies := setCookies(t, get)
	if len(cookies) != 1 {
		t.Fatalf("the response sets %d cookies, want the one that clears the flash", len(cookies))
	}
	c := cookies[0]
	if c.Name != FlashName || c.MaxAge >= 0 || c.Value != "" || c.Path != "/" {
		t.Errorf("the response sets %+v, want an expired, empty %s cookie on /", c, FlashName)
	}
}

func TestAddFlashAfterASameRequestReadStartsAgain(t *testing.T) {
	b := newBase()
	addFlashes(t, flashCodec, b, Flash{Kind: "info", Message: "a"})
	flashCodec.Flashes(b)
	addFlashes(t, flashCodec, b, Flash{Kind: "info", Message: "b"})

	wantFlashes(t, flashCodec.Flashes(flashRequest(t, b)), []Flash{{Kind: "info", Message: "b"}})
}

func flashRoutes(r *router.Router[*appCtx]) {
	r.POST("/users", func(c *appCtx) error {
		if err := c.Cookies.AddFlash(c, Flash{Kind: "success", Message: "saved"}); err != nil {
			return err
		}
		return c.Redirect(http.StatusSeeOther, "/users")
	})
	r.GET("/users", func(c *appCtx) error {
		return c.String(http.StatusOK, fmt.Sprintf("%v", c.Cookies.Flashes(c)))
	})
}

func TestFlashCrossesARedirectThroughARouter(t *testing.T) {
	for _, tc := range testRouters(testCodec()) {
		t.Run(tc.name, func(t *testing.T) {
			flashRoutes(tc.r)

			post := httptest.NewRecorder()
			tc.r.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/users", nil))
			if post.Code != http.StatusSeeOther {
				t.Fatalf("POST /users = %d, want 303; body: %s", post.Code, post.Body)
			}
			req := httptest.NewRequest(http.MethodGet, "/users", nil)
			for _, c := range post.Result().Cookies() {
				req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
			}
			rec := httptest.NewRecorder()
			tc.r.ServeHTTP(rec, req)
			if got, want := rec.Body.String(), "[{success saved}]"; got != want {
				t.Errorf("GET /users with the cookie = %q, want %q", got, want)
			}

			rec = httptest.NewRecorder()
			tc.r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users", nil))
			if got := rec.Body.String(); got != "[]" {
				t.Errorf("GET /users without the cookie = %q, want []", got)
			}
		})
	}
}

func TestHXRedirectCarriesTheFlash(t *testing.T) {
	cc := testCodec()
	r := router.New(func(http.ResponseWriter, *http.Request) *appCtx { return &appCtx{Cookies: cc} })
	r.POST("/join", func(c *appCtx) error {
		if err := c.Cookies.AddFlash(c, Flash{Kind: "success", Message: "welcome"}); err != nil {
			return err
		}
		return htmx.NewResponse(c).Redirect("/chat")
	})
	r.GET("/chat", func(c *appCtx) error { return c.String(http.StatusOK, fmt.Sprintf("%v", c.Cookies.Flashes(c))) })

	for _, tc := range []struct {
		name   string
		htmx   bool
		status int
	}{
		{"htmx gets a client-side redirect", true, http.StatusOK},
		{"a browser gets a 303", false, http.StatusSeeOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/join", nil)
			if tc.htmx {
				req.Header.Set(htmx.HeaderRequest, "true")
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			line := rec.Header().Get(router.HeaderSetCookie)
			if !strings.HasPrefix(line, FlashName+"=") {
				t.Fatalf("the redirect carries Set-Cookie %q, want the flash cookie", line)
			}
			c, err := http.ParseSetCookie(line)
			if err != nil {
				t.Fatalf("the Set-Cookie does not parse: %q: %v", line, err)
			}

			next := httptest.NewRequest(http.MethodGet, "/chat", nil)
			next.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
			got := httptest.NewRecorder()
			r.ServeHTTP(got, next)
			if want := "[{success welcome}]"; got.Body.String() != want {
				t.Errorf("the next page shows %q, want %q", got.Body, want)
			}
		})
	}
}
