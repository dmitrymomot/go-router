package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
)

const (
	apex = baseDomain + ":8080"

	testPassword = "correct horse"
)

func newTestRouter(t *testing.T) *router.Router[Ctx] {
	t.Helper()
	return newRouter(NewStore(), router.NewCookieCodec([]byte(strings.Repeat("k", 32))))
}

func host(slug string) string { return slug + "." + apex }

// browser is a fresh browser that starts on at, with no cookie yet.
func browser(t *testing.T, h http.Handler, at string) *routertest.Client {
	t.Helper()
	return routertest.NewClient(t, h, routertest.Host(at))
}

// postForm reads the form page at path, as a browser would, and posts form
// back to it with the CSRF token the page issued.
func postForm(t *testing.T, cl *routertest.Client, path string, form url.Values) *routertest.Response {
	t.Helper()

	cl.Get(path)
	token := cl.Cookie("_csrf")
	if token == nil {
		t.Fatalf("%s issued no CSRF cookie", path)
	}
	form.Set("_csrf", token.Value)
	return cl.Do(http.MethodPost, path, routertest.FormBody(form))
}

// signUp creates a workspace from the apex. The answer sends the browser on to
// the workspace host.
func signUp(t *testing.T, cl *routertest.Client, name, email, password string) *routertest.Response {
	t.Helper()
	return postForm(t, cl, "/signup", url.Values{
		"name":     {name},
		"email":    {email},
		"password": {password},
	})
}

func logIn(t *testing.T, cl *routertest.Client, email, password string) *routertest.Response {
	t.Helper()
	return postForm(t, cl, "/login", url.Values{"email": {email}, "password": {password}})
}

// sessionOf reports the session cookie that res sets, with the attributes the
// jar of a client drops.
func sessionOf(t *testing.T, res *routertest.Response) *http.Cookie {
	t.Helper()

	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("the response set no session cookie: %s", res)
	return nil
}

func TestTheApexAnswersOnItself(t *testing.T) {
	h := newTestRouter(t)

	res := browser(t, h, apex).Get("/")
	res.Expect(t).Status(http.StatusOK)
	if !strings.Contains(res.String(), "Create a workspace") {
		t.Errorf("the landing page has no signup link")
	}
}

func TestWWWSendsEveryRequestToTheApex(t *testing.T) {
	h := newTestRouter(t)

	for _, target := range []string{"/", "/signup", "/signup?plan=pro&ref=a%20b", "/nothing/here"} {
		res := routertest.Get(h, "http://www."+apex+target, routertest.Host("www."+apex))
		res.Expect(t).Redirect(http.StatusMovedPermanently, "http://"+apex+target)
	}
}

func TestTheApexHasNoDoorOfItsOwn(t *testing.T) {
	h := newTestRouter(t)

	for _, path := range []string{"/login", "/enter"} {
		routertest.Get(h, "http://"+apex+path, routertest.Host(apex)).
			Expect(t).Status(http.StatusNotFound)
	}
}

func TestSignupHandsTheOwnerToTheWorkspaceHost(t *testing.T) {
	h := newTestRouter(t)

	cl := browser(t, h, apex)
	made := signUp(t, cl, "Acme, Inc.", "ann@example.com", testPassword)
	made.Expect(t).Status(http.StatusSeeOther)

	location := made.Header.Get(router.HeaderLocation)
	if !strings.HasPrefix(location, "http://acme-inc."+apex+"/enter?ticket=") {
		t.Fatalf("signup redirected to %q, want a ticket on the workspace host", location)
	}
	// The apex cannot set a cookie for a host below it, so it sets none.
	for _, c := range made.Cookies() {
		if c.Name == sessionCookie {
			t.Error("the apex started a session it cannot own")
		}
	}

	entered := cl.Follow(made)
	entered.Expect(t).Redirect(http.StatusSeeOther, "/")

	// No Domain: the session belongs to this workspace host alone.
	if got := sessionOf(t, entered); got.Domain != "" || !got.HttpOnly {
		t.Errorf("session cookie = %+v, want no Domain and HttpOnly", got)
	}
}

func TestATicketWorksOnceAndOnItsOwnHost(t *testing.T) {
	h := newTestRouter(t)
	made := signUp(t, browser(t, h, apex), "Acme", "ann@example.com", testPassword)

	browser(t, h, apex).Follow(made).Expect(t).Redirect(http.StatusSeeOther, "/")

	// Spent. A second visit, from another browser, is sent to the door instead.
	again := browser(t, h, apex).Follow(made)
	again.Expect(t).Redirect(http.StatusSeeOther, "/login")
	for _, c := range again.Cookies() {
		if c.Name == sessionCookie && c.MaxAge >= 0 {
			t.Error("a spent ticket still started a session")
		}
	}
}

func TestATicketOfOneWorkspaceIsNoUseAtAnother(t *testing.T) {
	h := newTestRouter(t)
	signUp(t, browser(t, h, apex), "Beta", "bob@example.com", testPassword)
	made := signUp(t, browser(t, h, apex), "Acme", "ann@example.com", testPassword)

	u, err := made.Location()
	if err != nil {
		t.Fatal(err)
	}
	res := browser(t, h, host("beta")).Get("/enter?" + u.RawQuery)
	res.Expect(t).Redirect(http.StatusSeeOther, "/login")
}

func TestTheWorkspaceAnswersOnItsOwnHost(t *testing.T) {
	h := newTestRouter(t)
	cl := browser(t, h, apex)
	cl.Follow(signUp(t, cl, "Acme", "ann@example.com", testPassword))

	at := host("acme")
	owner := cl.Get("http://" + at + "/")
	owner.Expect(t).Status(http.StatusOK)
	if !strings.Contains(owner.String(), "ann@example.com") {
		t.Errorf("the dashboard does not name the signed-in account: %s", owner)
	}

	guest := browser(t, h, at).Get("/")
	guest.Expect(t).Status(http.StatusOK)
	if !strings.Contains(guest.String(), "as a guest") {
		t.Error("an anonymous reader is not told they are a guest")
	}
	if !strings.Contains(guest.String(), `href="/login"`) {
		t.Error("a guest is not offered the login form of this workspace")
	}
}

func TestLoginBelongsToTheWorkspace(t *testing.T) {
	h := newTestRouter(t)
	signUp(t, browser(t, h, apex), "Acme", "ann@example.com", testPassword)

	cl := browser(t, h, host("acme"))
	form := cl.Get("/login")
	form.Expect(t).Status(http.StatusOK)
	if !strings.Contains(form.String(), "Sign in to Acme") {
		t.Errorf("the login form does not name its workspace: %s", form)
	}

	res := logIn(t, cl, "ann@example.com", testPassword)
	res.Expect(t).Redirect(http.StatusSeeOther, "/")
	if got := sessionOf(t, res); got.Domain != "" {
		t.Errorf("session cookie domain = %q, want none", got.Domain)
	}
	if email, ok := routertest.SignedCookie(res, sessionCookie); !ok || email != "ann@example.com" {
		t.Errorf("the session carries %q, %v, want ann@example.com", email, ok)
	}
}

func TestAnAccountOfOneWorkspaceCannotOpenAnother(t *testing.T) {
	h := newTestRouter(t)
	signUp(t, browser(t, h, apex), "Acme", "ann@example.com", testPassword)
	signUp(t, browser(t, h, apex), "Beta", "ann@example.com", "another password")

	// The same address holds two accounts, and they are two accounts.
	logIn(t, browser(t, h, host("beta")), "ann@example.com", "another password").
		Expect(t).Status(http.StatusSeeOther)
	logIn(t, browser(t, h, host("beta")), "ann@example.com", testPassword).
		Expect(t).Status(http.StatusUnprocessableEntity)
}

func TestLoginRefusesAWrongPasswordAndAnUnknownEmail(t *testing.T) {
	h := newTestRouter(t)
	signUp(t, browser(t, h, apex), "Acme", "ann@example.com", testPassword)

	wrong := logIn(t, browser(t, h, host("acme")), "ann@example.com", "not the password")
	unknown := logIn(t, browser(t, h, host("acme")), "nobody@example.com", testPassword)

	// The same answer either way: the form must not say who has an account.
	for _, res := range []*routertest.Response{wrong, unknown} {
		res.Expect(t).Status(http.StatusUnprocessableEntity)
		if !strings.Contains(res.String(), "do not match") {
			t.Errorf("the form does not refuse the credentials: %s", res)
		}
		for _, c := range res.Cookies() {
			if c.Name == sessionCookie && c.MaxAge >= 0 {
				t.Error("a refused login still set a session")
			}
		}
	}
}

func TestSignoutSendsTheReaderBackToTheDoor(t *testing.T) {
	h := newTestRouter(t)
	signUp(t, browser(t, h, apex), "Acme", "ann@example.com", testPassword)

	cl := browser(t, h, host("acme"))
	logIn(t, cl, "ann@example.com", testPassword)

	// Read the dashboard and post its own sign-out form, token and all. A
	// form that carries no token has to fail here, as it does in a browser.
	page := cl.Get("/")
	page.Expect(t).Status(http.StatusOK)

	res := cl.Do(http.MethodPost, "/signout",
		routertest.FormBody(url.Values{"_csrf": {hiddenToken(t, page.String())}}))

	res.Expect(t).Redirect(http.StatusSeeOther, "/login")
	if got := sessionOf(t, res); got.MaxAge >= 0 {
		t.Errorf("sign out left the session alive: %+v", got)
	}
	if cl.Cookie(sessionCookie) != nil {
		t.Error("the browser still holds the session after sign out")
	}
}

func hiddenToken(t *testing.T, body string) string {
	t.Helper()

	_, rest, ok := strings.Cut(body, `name="_csrf" value="`)
	if !ok {
		t.Fatalf("the page carries no _csrf field: %s", body)
	}
	token, _, _ := strings.Cut(rest, `"`)
	if token == "" {
		t.Fatalf("the _csrf field of the page is empty: %s", body)
	}
	return token
}

func TestAnUnknownSubdomainIsNotFound(t *testing.T) {
	h := newTestRouter(t)

	at := host("nope")
	routertest.Get(h, "http://"+at+"/", routertest.Host(at)).
		Expect(t).Status(http.StatusNotFound)
}

func TestAnUnknownHostSaysWhichHostsAnswer(t *testing.T) {
	h := newTestRouter(t)

	res := routertest.Get(h, "http://127.0.0.1:8080/", routertest.Host("127.0.0.1:8080"))
	res.Expect(t).Status(http.StatusNotFound)
	if !strings.Contains(res.String(), baseDomain) {
		t.Errorf("the unknown-host page does not name the base domain: %s", res)
	}
}

func TestSignupRefusesANameWithoutLettersAndAReservedOne(t *testing.T) {
	h := newTestRouter(t)

	for _, name := range []string{"!!!", "WWW"} {
		res := signUp(t, browser(t, h, apex), name, "ann@example.com", testPassword)
		res.Expect(t).Status(http.StatusUnprocessableEntity)
		if !strings.Contains(res.String(), "Pick another name") {
			t.Errorf("%q: the form does not say why it refused: %s", name, res)
		}
	}
}

func TestSignupRefusesATakenSubdomain(t *testing.T) {
	h := newTestRouter(t)
	signUp(t, browser(t, h, apex), "Acme", "ann@example.com", testPassword)

	res := signUp(t, browser(t, h, apex), "acme", "bob@example.com", testPassword)
	res.Expect(t).Status(http.StatusUnprocessableEntity)
	if !strings.Contains(res.String(), "taken") {
		t.Errorf("the form does not say the subdomain is taken: %s", res)
	}
}

func TestSignupNeedsAPasswordOfEightCharacters(t *testing.T) {
	h := newTestRouter(t)

	res := signUp(t, browser(t, h, apex), "Acme", "ann@example.com", "short")
	res.Expect(t).Status(http.StatusUnprocessableEntity)
	if !strings.Contains(res.String(), "at least 8 characters") {
		t.Errorf("the form does not name the password rule: %s", res)
	}
}

func TestSignupNeedsTheCSRFToken(t *testing.T) {
	h := newTestRouter(t)

	res := browser(t, h, apex).Do(http.MethodPost, "/signup",
		routertest.FormBody(url.Values{
			"name": {"Acme"}, "email": {"ann@example.com"}, "password": {testPassword},
		}))
	res.Expect(t).Status(http.StatusForbidden)
}
