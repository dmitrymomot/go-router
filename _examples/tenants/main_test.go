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
	apex      = baseDomain + ":8080"
	signupURL = "http://" + apex + "/signup"

	testPassword = "correct horse"
)

func newTestRouter(t *testing.T) *router.Router[Ctx] {
	t.Helper()
	return newRouter(NewStore(), router.NewCookieCodec([]byte(strings.Repeat("k", 32))))
}

func host(slug string) string { return slug + "." + apex }

// csrf issues a token by asking for a page, and gives back the cookie that
// signs it. The token is the value of that cookie.
func csrf(t *testing.T, h http.Handler, atHost, target string) *http.Cookie {
	t.Helper()

	res := routertest.Get(h, target, routertest.Host(atHost))
	for _, c := range res.Cookies() {
		if c.Name == "_csrf" {
			return c
		}
	}
	t.Fatalf("%s issued no CSRF cookie (status %d)", target, res.StatusCode)
	return nil
}

func postTo(t *testing.T, h http.Handler, atHost, target string, form url.Values) *routertest.Response {
	t.Helper()

	token := csrf(t, h, atHost, target)
	form.Set("_csrf", token.Value)
	return routertest.Do(h, http.MethodPost, target,
		routertest.Host(atHost),
		routertest.Cookie(token),
		routertest.FormBody(form))
}

func signUp(t *testing.T, h http.Handler, name, email, password string) *routertest.Response {
	t.Helper()
	return postTo(t, h, apex, signupURL, url.Values{
		"name":     {name},
		"email":    {email},
		"password": {password},
	})
}

func logIn(t *testing.T, h http.Handler, slug, email, password string) *routertest.Response {
	t.Helper()

	at := host(slug)
	return postTo(t, h, at, "http://"+at+"/login",
		url.Values{"email": {email}, "password": {password}})
}

func enterWith(t *testing.T, h http.Handler, location string) *routertest.Response {
	t.Helper()

	u, err := url.Parse(location)
	if err != nil {
		t.Fatalf("signup redirected to %q: %v", location, err)
	}
	return routertest.Get(h, location, routertest.Host(u.Host))
}

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

func TestTheApexAnswersOnItselfAndOnWWW(t *testing.T) {
	h := newTestRouter(t)

	for _, at := range []string{apex, "www." + apex} {
		res := routertest.Get(h, "http://"+at+"/", routertest.Host(at))
		res.AssertStatus(t, http.StatusOK)
		if !strings.Contains(res.String(), "Create a workspace") {
			t.Errorf("%s: the landing page has no signup link", at)
		}
	}
}

func TestTheApexHasNoDoorOfItsOwn(t *testing.T) {
	h := newTestRouter(t)

	for _, path := range []string{"/login", "/enter"} {
		routertest.Get(h, "http://"+apex+path, routertest.Host(apex)).
			AssertStatus(t, http.StatusNotFound)
	}
}

func TestSignupHandsTheOwnerToTheWorkspaceHost(t *testing.T) {
	h := newTestRouter(t)

	made := signUp(t, h, "Acme, Inc.", "ann@example.com", testPassword)
	made.AssertStatus(t, http.StatusSeeOther)

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

	entered := enterWith(t, h, location)
	entered.AssertStatus(t, http.StatusSeeOther)
	entered.AssertHeader(t, router.HeaderLocation, "/")

	// No Domain: the session belongs to this workspace host alone.
	if got := sessionOf(t, entered); got.Domain != "" || !got.HttpOnly {
		t.Errorf("session cookie = %+v, want no Domain and HttpOnly", got)
	}
}

func TestATicketWorksOnceAndOnItsOwnHost(t *testing.T) {
	h := newTestRouter(t)
	location := signUp(t, h, "Acme", "ann@example.com", testPassword).Header.Get(router.HeaderLocation)

	enterWith(t, h, location).AssertHeader(t, router.HeaderLocation, "/")

	// Spent. A second visit is sent to the door instead.
	again := enterWith(t, h, location)
	again.AssertHeader(t, router.HeaderLocation, "/login")
	for _, c := range again.Cookies() {
		if c.Name == sessionCookie && c.MaxAge >= 0 {
			t.Error("a spent ticket still started a session")
		}
	}
}

func TestATicketOfOneWorkspaceIsNoUseAtAnother(t *testing.T) {
	h := newTestRouter(t)
	signUp(t, h, "Beta", "bob@example.com", testPassword)
	location := signUp(t, h, "Acme", "ann@example.com", testPassword).Header.Get(router.HeaderLocation)

	u, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	stolen := "http://" + host("beta") + "/enter?" + u.RawQuery
	res := routertest.Get(h, stolen, routertest.Host(host("beta")))
	res.AssertHeader(t, router.HeaderLocation, "/login")
}

func TestTheWorkspaceAnswersOnItsOwnHost(t *testing.T) {
	h := newTestRouter(t)
	location := signUp(t, h, "Acme", "ann@example.com", testPassword).Header.Get(router.HeaderLocation)
	session := sessionOf(t, enterWith(t, h, location))

	at := host("acme")
	owner := routertest.Get(h, "http://"+at+"/", routertest.Host(at), routertest.Cookie(session))
	owner.AssertStatus(t, http.StatusOK)
	if !strings.Contains(owner.String(), "ann@example.com") {
		t.Errorf("the dashboard does not name the signed-in account: %s", owner)
	}

	guest := routertest.Get(h, "http://"+at+"/", routertest.Host(at))
	guest.AssertStatus(t, http.StatusOK)
	if !strings.Contains(guest.String(), "as a guest") {
		t.Error("an anonymous reader is not told they are a guest")
	}
	if !strings.Contains(guest.String(), `href="/login"`) {
		t.Error("a guest is not offered the login form of this workspace")
	}
}

func TestLoginBelongsToTheWorkspace(t *testing.T) {
	h := newTestRouter(t)
	signUp(t, h, "Acme", "ann@example.com", testPassword)

	at := host("acme")
	form := routertest.Get(h, "http://"+at+"/login", routertest.Host(at))
	form.AssertStatus(t, http.StatusOK)
	if !strings.Contains(form.String(), "Sign in to Acme") {
		t.Errorf("the login form does not name its workspace: %s", form)
	}

	res := logIn(t, h, "acme", "ann@example.com", testPassword)
	res.AssertStatus(t, http.StatusSeeOther)
	res.AssertHeader(t, router.HeaderLocation, "/")
	if got := sessionOf(t, res); got.Domain != "" {
		t.Errorf("session cookie domain = %q, want none", got.Domain)
	}
}

func TestAnAccountOfOneWorkspaceCannotOpenAnother(t *testing.T) {
	h := newTestRouter(t)
	signUp(t, h, "Acme", "ann@example.com", testPassword)
	signUp(t, h, "Beta", "ann@example.com", "another password")

	// The same address holds two accounts, and they are two accounts.
	logIn(t, h, "beta", "ann@example.com", "another password").
		AssertStatus(t, http.StatusSeeOther)
	logIn(t, h, "beta", "ann@example.com", testPassword).
		AssertStatus(t, http.StatusUnprocessableEntity)
}

func TestLoginRefusesAWrongPasswordAndAnUnknownEmail(t *testing.T) {
	h := newTestRouter(t)
	signUp(t, h, "Acme", "ann@example.com", testPassword)

	wrong := logIn(t, h, "acme", "ann@example.com", "not the password")
	unknown := logIn(t, h, "acme", "nobody@example.com", testPassword)

	// The same answer either way: the form must not say who has an account.
	for _, res := range []*routertest.Response{wrong, unknown} {
		res.AssertStatus(t, http.StatusUnprocessableEntity)
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
	signUp(t, h, "Acme", "ann@example.com", testPassword)

	signedIn := logIn(t, h, "acme", "ann@example.com", testPassword)
	session := sessionOf(t, signedIn)

	// Read the dashboard and post its own sign-out form, token and all. A
	// form that carries no token has to fail here, as it does in a browser.
	at := host("acme")
	page := routertest.Get(h, "http://"+at+"/", routertest.Host(at), routertest.Cookie(session))
	page.AssertStatus(t, http.StatusOK)

	res := routertest.Do(h, http.MethodPost, "http://"+at+"/signout",
		routertest.Host(at),
		routertest.Cookie(csrfCookieOf(t, page)),
		routertest.Cookie(session),
		routertest.FormBody(url.Values{"_csrf": {hiddenToken(t, page.String())}}))

	res.AssertStatus(t, http.StatusSeeOther)
	res.AssertHeader(t, router.HeaderLocation, "/login")
	if got := sessionOf(t, res); got.MaxAge >= 0 {
		t.Errorf("sign out left the session alive: %+v", got)
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

func csrfCookieOf(t *testing.T, res *routertest.Response) *http.Cookie {
	t.Helper()

	for _, c := range res.Cookies() {
		if c.Name == "_csrf" {
			return c
		}
	}
	t.Fatal("the page issued no CSRF cookie")
	return nil
}

func TestAnUnknownSubdomainIsNotFound(t *testing.T) {
	h := newTestRouter(t)

	at := host("nope")
	routertest.Get(h, "http://"+at+"/", routertest.Host(at)).
		AssertStatus(t, http.StatusNotFound)
}

func TestAnUnknownHostSaysWhichHostsAnswer(t *testing.T) {
	h := newTestRouter(t)

	res := routertest.Get(h, "http://127.0.0.1:8080/", routertest.Host("127.0.0.1:8080"))
	res.AssertStatus(t, http.StatusNotFound)
	if !strings.Contains(res.String(), baseDomain) {
		t.Errorf("the unknown-host page does not name the base domain: %s", res)
	}
}

func TestSignupRefusesANameWithoutLettersAndAReservedOne(t *testing.T) {
	h := newTestRouter(t)

	for _, name := range []string{"!!!", "WWW"} {
		res := signUp(t, h, name, "ann@example.com", testPassword)
		res.AssertStatus(t, http.StatusUnprocessableEntity)
		if !strings.Contains(res.String(), "Pick another name") {
			t.Errorf("%q: the form does not say why it refused: %s", name, res)
		}
	}
}

func TestSignupRefusesATakenSubdomain(t *testing.T) {
	h := newTestRouter(t)
	signUp(t, h, "Acme", "ann@example.com", testPassword)

	res := signUp(t, h, "acme", "bob@example.com", testPassword)
	res.AssertStatus(t, http.StatusUnprocessableEntity)
	if !strings.Contains(res.String(), "taken") {
		t.Errorf("the form does not say the subdomain is taken: %s", res)
	}
}

func TestSignupNeedsAPasswordOfEightCharacters(t *testing.T) {
	h := newTestRouter(t)

	res := signUp(t, h, "Acme", "ann@example.com", "short")
	res.AssertStatus(t, http.StatusUnprocessableEntity)
	if !strings.Contains(res.String(), "at least 8 characters") {
		t.Errorf("the form does not name the password rule: %s", res)
	}
}

func TestSignupNeedsTheCSRFToken(t *testing.T) {
	h := newTestRouter(t)

	res := routertest.Do(h, http.MethodPost, signupURL, routertest.Host(apex),
		routertest.FormBody(url.Values{
			"name": {"Acme"}, "email": {"ann@example.com"}, "password": {testPassword},
		}))
	res.AssertStatus(t, http.StatusForbidden)
}
