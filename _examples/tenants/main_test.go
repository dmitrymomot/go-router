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
	loginURL  = "http://" + apex + "/login"

	testPassword = "correct horse"
)

func newTestRouter(t *testing.T) *router.Router[Ctx] {
	t.Helper()
	return newRouter(NewStore(), router.NewCookieCodec([]byte(strings.Repeat("k", 32))))
}

// csrf issues a token by asking for the form, and gives back the cookie that
// signs it. The token is the value of that cookie.
func csrf(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()

	res := routertest.Get(h, signupURL, routertest.Host(apex))
	res.AssertStatus(t, http.StatusOK)
	for _, c := range res.Cookies() {
		if c.Name == "_csrf" {
			return c
		}
	}
	t.Fatal("the signup form issued no CSRF cookie")
	return nil
}

func signUp(t *testing.T, h http.Handler, name, email string) *routertest.Response {
	t.Helper()
	return post(t, h, signupURL, url.Values{
		"name":     {name},
		"email":    {email},
		"password": {testPassword},
	})
}

func logIn(t *testing.T, h http.Handler, email, password string) *routertest.Response {
	t.Helper()
	return post(t, h, loginURL, url.Values{"email": {email}, "password": {password}})
}

// post sends a form with a CSRF token that matches its cookie.
func post(t *testing.T, h http.Handler, target string, form url.Values) *routertest.Response {
	t.Helper()

	token := csrf(t, h)
	form.Set("_csrf", token.Value)
	return routertest.Do(h, http.MethodPost, target,
		routertest.Host(apex),
		routertest.Cookie(token),
		routertest.FormBody(form))
}

func TestTheApexAnswersOnItselfAndOnWWW(t *testing.T) {
	h := newTestRouter(t)

	for _, host := range []string{apex, "www." + apex} {
		res := routertest.Get(h, "http://"+host+"/", routertest.Host(host))
		res.AssertStatus(t, http.StatusOK)
		if !strings.Contains(res.String(), "Create a workspace") {
			t.Errorf("%s: the landing page has no signup link", host)
		}
	}
}

func TestSignupRedirectsToTheNewSubdomain(t *testing.T) {
	h := newTestRouter(t)

	res := signUp(t, h, "Acme, Inc.", "ann@example.com")
	res.AssertStatus(t, http.StatusSeeOther)
	res.AssertHeader(t, router.HeaderLocation, "http://acme-inc."+apex+"/")

	// The session must reach every subdomain, so it names the base domain.
	var session *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil {
		t.Fatal("signup set no session cookie")
	}
	if session.Domain != baseDomain || !session.HttpOnly {
		t.Errorf("session cookie = %+v, want Domain %q and HttpOnly", session, baseDomain)
	}
}

func TestTheWorkspaceAnswersOnItsOwnHost(t *testing.T) {
	h := newTestRouter(t)
	session := sessionOf(t, signUp(t, h, "Acme", "ann@example.com"))

	host := "acme." + apex
	owner := routertest.Get(h, "http://"+host+"/", routertest.Host(host), routertest.Cookie(session))
	owner.AssertStatus(t, http.StatusOK)
	if !strings.Contains(owner.String(), "acme.lvh.me") {
		t.Errorf("the dashboard does not name its host: %s", owner)
	}

	guest := routertest.Get(h, "http://"+host+"/", routertest.Host(host))
	guest.AssertStatus(t, http.StatusOK)
	if !strings.Contains(guest.String(), "as a guest") {
		t.Error("an anonymous reader is not told they are a guest")
	}
}

func TestAnUnknownSubdomainIsNotFound(t *testing.T) {
	h := newTestRouter(t)

	host := "nope." + apex
	routertest.Get(h, "http://"+host+"/", routertest.Host(host)).
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

func TestANameWithoutLettersIsRefused(t *testing.T) {
	h := newTestRouter(t)

	res := signUp(t, h, "!!!", "ann@example.com")
	res.AssertStatus(t, http.StatusUnprocessableEntity)
	if !strings.Contains(res.String(), "Pick another name") {
		t.Errorf("the form does not say why it refused: %s", res)
	}
}

func TestAReservedSubdomainIsRefused(t *testing.T) {
	h := newTestRouter(t)

	res := signUp(t, h, "WWW", "ann@example.com")
	res.AssertStatus(t, http.StatusUnprocessableEntity)
	if !strings.Contains(res.String(), "reserved") {
		t.Errorf("the form does not say the subdomain is reserved: %s", res)
	}
}

func TestSignupNeedsTheCSRFToken(t *testing.T) {
	h := newTestRouter(t)

	res := routertest.Do(h, http.MethodPost, signupURL, routertest.Host(apex),
		routertest.FormBody(url.Values{"name": {"Acme"}, "email": {"ann@example.com"}}))
	res.AssertStatus(t, http.StatusForbidden)
}

func sessionOf(t *testing.T, res *routertest.Response) *http.Cookie {
	t.Helper()

	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("the response set no session cookie")
	return nil
}

func TestLoginTakesAnOwnerToTheirFirstWorkspace(t *testing.T) {
	h := newTestRouter(t)
	signUp(t, h, "Acme", "ann@example.com").AssertStatus(t, http.StatusSeeOther)

	res := logIn(t, h, "ann@example.com", testPassword)
	res.AssertStatus(t, http.StatusSeeOther)
	res.AssertHeader(t, router.HeaderLocation, "http://acme."+apex+"/")

	if got := sessionOf(t, res); got.Domain != baseDomain {
		t.Errorf("session cookie domain = %q, want %q", got.Domain, baseDomain)
	}
}

func TestLoginRefusesAWrongPasswordAndAnUnknownEmail(t *testing.T) {
	h := newTestRouter(t)
	signUp(t, h, "Acme", "ann@example.com")

	wrong := logIn(t, h, "ann@example.com", "not the password")
	unknown := logIn(t, h, "nobody@example.com", testPassword)

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

func TestSignupRefusesAnEmailThatAlreadyHasAnAccount(t *testing.T) {
	h := newTestRouter(t)
	signUp(t, h, "Acme", "ann@example.com")

	res := signUp(t, h, "Beta", "ann@example.com")
	res.AssertStatus(t, http.StatusUnprocessableEntity)
	if !strings.Contains(res.String(), "Sign in instead") {
		t.Errorf("the form does not send them to the login page: %s", res)
	}
}

func TestSignupNeedsAPasswordOfEightCharacters(t *testing.T) {
	h := newTestRouter(t)

	res := post(t, h, signupURL, url.Values{
		"name": {"Acme"}, "email": {"ann@example.com"}, "password": {"short"},
	})
	res.AssertStatus(t, http.StatusUnprocessableEntity)
	if !strings.Contains(res.String(), "at least 8 characters") {
		t.Errorf("the form does not name the password rule: %s", res)
	}
}

func TestAnAnonymousReaderCannotAddAWorkspace(t *testing.T) {
	h := newTestRouter(t)

	res := post(t, h, "http://"+apex+"/workspaces", url.Values{"name": {"Beta"}})
	res.AssertStatus(t, http.StatusSeeOther)
	res.AssertHeader(t, router.HeaderLocation, "/login")
}

func TestASignedInOwnerAddsASecondWorkspace(t *testing.T) {
	h := newTestRouter(t)
	session := sessionOf(t, signUp(t, h, "Acme", "ann@example.com"))

	token := csrf(t, h)
	res := routertest.Do(h, http.MethodPost, "http://"+apex+"/workspaces",
		routertest.Host(apex),
		routertest.Cookie(token),
		routertest.Cookie(session),
		routertest.FormBody(url.Values{"_csrf": {token.Value}, "name": {"Beta, Ltd."}}))

	res.AssertStatus(t, http.StatusSeeOther)
	res.AssertHeader(t, router.HeaderLocation, "http://beta-ltd."+apex+"/")
}
