package routertest

import (
	"cmp"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
)

// Client sends requests to one handler in process, as a browser would. Every
// request carries the defaults of the client and the cookies its jar holds for
// the URL, and every Set-Cookie of an answer updates the jar.
//
// The jar honors Path, Domain, Expires and Secure: a Secure cookie goes back
// only to a request that [router.SchemeOf] counts as https. It knows no public
// suffix list, which test hosts do not need.
//
// A Client is safe for concurrent use and never fails the test itself, so
// subtests can share one. A stream that the test reads as it arrives still
// needs [NewServer].
type Client struct {
	tb       testing.TB
	h        http.Handler
	jar      *cookiejar.Jar
	origin   *url.URL
	defaults []RequestOption
}

// NewClient builds a client for h. opts apply to every request, before the
// options of the request itself, and every request carries tb.Context().
//
// NewClient fails the test if h is nil.
func NewClient(tb testing.TB, h http.Handler, opts ...RequestOption) *Client {
	tb.Helper()
	if h == nil {
		tb.Fatalf("routertest: NewClient needs a handler")
		return nil
	}
	// cookiejar.New fails only on options it is given, and it gets none.
	jar, _ := cookiejar.New(nil)
	defaults := slices.Clone(opts)
	origin := cookieURL(Request(http.MethodGet, "/", defaults...))
	origin.Path = "/"
	return &Client{tb: tb, h: h, jar: jar, origin: origin, defaults: defaults}
}

// Do sends one request and stores the cookies its answer sets. See [Request]
// for target. The host of an absolute target wins over a [Host] default.
func (c *Client) Do(method, target string, opts ...RequestOption) *Response {
	var hostOpt []RequestOption
	if u, err := url.Parse(target); err == nil && u.Scheme != "" && u.Host != "" {
		hostOpt = []RequestOption{Host(u.Host)}
	}
	req := Request(method, target,
		slices.Concat([]RequestOption{Context(c.tb.Context())}, c.defaults, hostOpt, opts)...)
	at := cookieURL(req)
	for _, ck := range c.jar.Cookies(at) {
		req.AddCookie(ck)
	}
	res := Serve(c.h, req)
	c.jar.SetCookies(at, res.Cookies())
	return res
}

// Get is [Client.Do] for a GET.
func (c *Client) Get(target string, opts ...RequestOption) *Response {
	return c.Do(http.MethodGet, target, opts...)
}

// Follow sends a GET to the Location of res, or to its HX-Redirect, resolved
// against the request that res answered. It follows one hop, and it sends a
// GET even after a 307 or a 308.
//
// Follow panics when res is nil or names neither header.
func (c *Client) Follow(res *Response) *Response {
	if res == nil {
		panic("routertest: Follow needs a response")
	}
	loc := cmp.Or(res.Header.Get(router.HeaderLocation), res.Header.Get(router.HeaderHXRedirect))
	if loc == "" {
		panic("routertest: Follow needs a Location or an HX-Redirect, and the " +
			strconv.Itoa(res.StatusCode) + " answer has neither")
	}
	base := &url.URL{Path: "/"}
	if req := res.Request; req != nil {
		base = &url.URL{Scheme: router.SchemeOf(req), Host: req.Host, Path: req.URL.Path, RawPath: req.URL.RawPath}
	}
	u, err := base.Parse(loc)
	if err != nil {
		panic("routertest: Follow cannot read the Location " + strconv.Quote(loc) + ": " + err.Error())
	}
	u.Fragment, u.RawFragment = "", ""
	return c.Do(http.MethodGet, u.String())
}

// Cookie reports the cookie called name that the client would send to the
// root of its default host, with only its name and value, or nil.
func (c *Client) Cookie(name string) *http.Cookie {
	for _, ck := range c.jar.Cookies(c.origin) {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}

// SetCookie stores ck as if the default host had set it.
//
// SetCookie panics if ck is nil, or if the jar would never send it back, such
// as a Secure cookie over http or a Domain that does not cover the host.
func (c *Client) SetCookie(ck *http.Cookie) {
	if ck == nil {
		panic("routertest: SetCookie needs a cookie")
	}
	c.jar.SetCookies(c.origin, []*http.Cookie{ck})
	if ck.MaxAge < 0 || !ck.Expires.IsZero() && !ck.Expires.After(time.Now()) {
		return
	}
	at := *c.origin
	if strings.HasPrefix(ck.Path, "/") {
		at.Path = ck.Path
	}
	if ck.Domain != "" {
		at.Host = strings.TrimPrefix(ck.Domain, ".")
	}
	for _, got := range c.jar.Cookies(&at) {
		if got.Name == ck.Name {
			return
		}
	}
	panic("routertest: the jar refused the cookie " + ck.Name + " for " + c.origin.String())
}

// cookieURL is the URL the jar files the cookies of req under.
func cookieURL(req *http.Request) *url.URL {
	return &url.URL{Scheme: router.SchemeOf(req), Host: req.Host, Path: req.URL.Path}
}
