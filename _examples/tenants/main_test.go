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

	token := csrf(t, h)
	return routertest.Do(h, http.MethodPost, signupURL,
		routertest.Host(apex),
		routertest.Cookie(token),
		routertest.FormBody(url.Values{
			"_csrf": {token.Value},
			"name":  {name},
			"email": {email},
		}))
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
