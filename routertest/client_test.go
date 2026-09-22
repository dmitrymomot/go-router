package routertest_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/htmx"
	"github.com/dmitrymomot/go-router/routertest"
)

// echoCookies writes the cookies of r as "name=value" pairs in name order.
func echoCookies(w http.ResponseWriter, r *http.Request) {
	var pairs []string
	for _, c := range r.Cookies() {
		pairs = append(pairs, c.Name+"="+c.Value)
	}
	slices.Sort(pairs)
	_, _ = fmt.Fprint(w, strings.Join(pairs, " "))
}

func setting(ck *http.Cookie) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, ck)
		w.WriteHeader(http.StatusNoContent)
	}
}

func clientHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: r.FormValue("name"), Path: "/"})
		http.Redirect(w, r, "/me", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		name := "nobody"
		if c, err := r.Cookie("session"); err == nil {
			name = c.Value
		}
		_, _ = fmt.Fprint(w, "hello "+name)
	})
	mux.Handle("POST /logout", setting(&http.Cookie{Name: "session", Path: "/", MaxAge: -1}))
	mux.Handle("POST /expire", setting(&http.Cookie{Name: "session", Path: "/", Expires: time.Unix(1, 0)}))
	mux.Handle("GET /admin/set", setting(&http.Cookie{Name: "admin", Value: "1", Path: "/admin"}))
	mux.Handle("GET /secure", setting(&http.Cookie{Name: "sec", Value: "1", Path: "/", Secure: true}))
	mux.Handle("GET /host", setting(&http.Cookie{Name: "hostonly", Value: "1", Path: "/"}))
	mux.Handle("GET /domain", setting(&http.Cookie{Name: "dom", Value: "1", Path: "/", Domain: "example.test"}))
	mux.Handle("GET /fragment", setting(&http.Cookie{Name: "hx", Value: "1", Path: "/"}))
	mux.Handle("OPTIONS /preflight", setting(&http.Cookie{Name: "pre", Value: "1", Path: "/"}))
	mux.HandleFunc("/", echoCookies)
	mux.HandleFunc("GET /echo/{what}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("what") {
		case "host":
			_, _ = fmt.Fprint(w, r.Host)
		case "remote":
			_, _ = fmt.Fprint(w, r.RemoteAddr)
		case "accept":
			_, _ = fmt.Fprint(w, r.Header.Get("Accept"))
		}
	})
	redirect := func(w http.ResponseWriter, r *http.Request) {
		code := http.StatusSeeOther
		if r.URL.Query().Has("temp") {
			code = http.StatusTemporaryRedirect
		}
		w.Header().Set(router.HeaderLocation, r.URL.Query().Get("to"))
		w.WriteHeader(code)
	}
	mux.HandleFunc("/go", redirect)
	mux.HandleFunc("/a/go", redirect)
	mux.HandleFunc("GET /hx", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(htmx.HeaderRedirect, "/me")
	})
	method := func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, r.Method+" "+r.URL.RequestURI()+" "+r.Host+" "+router.SchemeOf(r))
	}
	mux.HandleFunc("/method", method)
	mux.HandleFunc("/a/method", method)
	return mux
}

func TestClientSendsBackTheCookiesItWasGiven(t *testing.T) {
	cl := routertest.NewClient(t, clientHandler())

	cl.Do(http.MethodPost, "/login", routertest.FormBody(url.Values{"name": {"ann"}})).
		Expect(t).Redirect(http.StatusSeeOther, "/me")
	cl.Get("/me").Expect(t).Body("hello ann")
	if got := cl.Cookie("session"); got == nil || got.Value != "ann" {
		t.Errorf("Cookie(session) = %v, want ann", got)
	}
}

func TestClientStoresTheCookiesOfEveryRequest(t *testing.T) {
	cl := routertest.NewClient(t, clientHandler())

	cl.Get("/fragment", routertest.HTMX())
	cl.Do(http.MethodOptions, "/preflight", routertest.Header("Access-Control-Request-Method", http.MethodPut))
	cl.Get("/").Expect(t).Body("hx=1 pre=1")
}

func TestClientDropsACookieTheHandlerClears(t *testing.T) {
	for _, path := range []string{"/logout", "/expire"} {
		t.Run(path, func(t *testing.T) {
			cl := routertest.NewClient(t, clientHandler())
			cl.Do(http.MethodPost, "/login", routertest.FormBody(url.Values{"name": {"ann"}}))
			cl.Do(http.MethodPost, path)

			if got := cl.Cookie("session"); got != nil {
				t.Errorf("Cookie(session) = %v, want nil", got)
			}
			cl.Get("/me").Expect(t).Body("hello nobody")
		})
	}
}

func TestClientHonorsPathAndHost(t *testing.T) {
	cl := routertest.NewClient(t, clientHandler())

	cl.Get("/admin/set")
	cl.Get("/admin/read").Expect(t).Body("admin=1")
	cl.Get("/public/read").Expect(t).Body("")

	cl.Get("/host", routertest.Host("a.example.test"))
	cl.Get("/domain", routertest.Host("a.example.test"))
	cl.Get("/", routertest.Host("a.example.test")).Expect(t).Body("dom=1 hostonly=1")
	cl.Get("/", routertest.Host("b.example.test")).Expect(t).Body("dom=1")
}

func TestClientSendsASecureCookieOverHTTPSOnly(t *testing.T) {
	cl := routertest.NewClient(t, clientHandler())
	cl.Get("https://example.com/secure")

	cl.Get("/").Expect(t).Body("")
	cl.Get("https://example.com/").Expect(t).Body("sec=1")

	proxied := routertest.NewClient(t, clientHandler(), routertest.Header(router.HeaderXForwardedProto, "https"))
	proxied.Get("/secure")
	proxied.Get("/").Expect(t).Body("sec=1")
	if proxied.Cookie("sec") == nil {
		t.Error("Cookie does not see a Secure cookie of an https client")
	}
}

func TestClientAppliesItsDefaultsFirst(t *testing.T) {
	cl := routertest.NewClient(t, clientHandler(),
		routertest.Host("app.example.test"),
		routertest.RemoteAddr("203.0.113.7:4321"),
		routertest.Header("Accept", router.MIMEApplicationJSON),
	)

	cl.Get("/echo/host").Expect(t).Body("app.example.test")
	cl.Get("/echo/remote").Expect(t).Body("203.0.113.7:4321")
	cl.Get("/echo/accept").Expect(t).Body(router.MIMEApplicationJSON)
	cl.Get("/echo/accept", routertest.Header("Accept", router.MIMETextPlain)).Expect(t).Body(router.MIMETextPlain)
	cl.Get("http://other.example.test/echo/host").Expect(t).Body("other.example.test")
}

func TestClientRequestsCarryTheContextOfTheTest(t *testing.T) {
	var got context.Context
	cl := routertest.NewClient(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Context()
	}))
	cl.Get("/")
	if got != t.Context() {
		t.Error("the request does not carry t.Context()")
	}
}

func TestClientSetCookieSeedsTheJar(t *testing.T) {
	cl := routertest.NewClient(t, clientHandler())

	cl.SetCookie(&http.Cookie{Name: "session", Value: "bob"})
	cl.Get("/me").Expect(t).Body("hello bob")
	if got := cl.Cookie("session"); got == nil || got.Value != "bob" {
		t.Errorf("Cookie(session) = %v, want bob", got)
	}

	cl.SetCookie(&http.Cookie{Name: "admin", Value: "1", Path: "/admin"})
	cl.Get("/admin/x").Expect(t).Body("admin=1 session=bob")

	cl.SetCookie(&http.Cookie{Name: "session", MaxAge: -1})
	cl.Get("/me").Expect(t).Body("hello nobody")
}

func TestClientSetCookiePanicsWhenTheJarRefuses(t *testing.T) {
	for _, ck := range []*http.Cookie{
		{Name: "sec", Value: "1", Secure: true},
		{Name: "dom", Value: "1", Domain: "other.test"},
	} {
		t.Run(ck.Name, func(t *testing.T) {
			defer func() {
				msg, _ := recover().(string)
				if !strings.Contains(msg, ck.Name) || !strings.Contains(msg, "http://example.com/") {
					t.Errorf("panic = %q, want one that names the cookie and the origin", msg)
				}
			}()
			routertest.NewClient(t, clientHandler()).SetCookie(ck)
		})
	}
}

func TestClientFollow(t *testing.T) {
	tests := []struct {
		name  string
		first func(*routertest.Client) *routertest.Response
		want  string
	}{
		{
			name:  "a relative 303",
			first: func(cl *routertest.Client) *routertest.Response { return cl.Get("/go?to=/method") },
			want:  "GET /method example.com http",
		},
		{
			name:  "a relative path",
			first: func(cl *routertest.Client) *routertest.Response { return cl.Get("/a/go?to=method") },
			want:  "GET /a/method example.com http",
		},
		{
			name: "HX-Redirect",
			first: func(cl *routertest.Client) *routertest.Response {
				cl.Do(http.MethodPost, "/login", routertest.FormBody(url.Values{"name": {"ann"}}))
				return cl.Get("/hx")
			},
			want: "hello ann",
		},
		{
			name: "a per-request host",
			first: func(cl *routertest.Client) *routertest.Response {
				return cl.Get("/go?to=/method", routertest.Host("b.example.test"))
			},
			want: "GET /method b.example.test http",
		},
		{
			name:  "an https original",
			first: func(cl *routertest.Client) *routertest.Response { return cl.Get("https://example.com/go?to=/method") },
			want:  "GET /method example.com https",
		},
		{
			name:  "a fragment",
			first: func(cl *routertest.Client) *routertest.Response { return cl.Get("/go?to=/method%3Fq=1%23top") },
			want:  "GET /method?q=1 example.com http",
		},
		{
			name: "a 307 after a POST",
			first: func(cl *routertest.Client) *routertest.Response {
				return cl.Do(http.MethodPost, "/go?temp=1&to=/method")
			},
			want: "GET /method example.com http",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := routertest.NewClient(t, clientHandler())
			cl.Follow(tt.first(cl)).Expect(t).Status(http.StatusOK).Body(tt.want)
		})
	}
}

func TestClientFollowCarriesTheCookiesOfTheTarget(t *testing.T) {
	cl := routertest.NewClient(t, clientHandler())
	cl.Get("/host", routertest.Host("b.example.test"))
	cl.Follow(cl.Get("/go?to=http://b.example.test/")).Expect(t).Body("hostonly=1")

	proxied := routertest.NewClient(t, clientHandler(), routertest.Header(router.HeaderXForwardedProto, "https"))
	proxied.Get("/secure")
	proxied.Follow(proxied.Get("/go?to=/")).Expect(t).Body("sec=1")
}

func TestClientFollowPanicsWithoutALocation(t *testing.T) {
	cl := routertest.NewClient(t, clientHandler())
	defer func() {
		msg, _ := recover().(string)
		if !strings.Contains(msg, "200") {
			t.Errorf("panic = %q, want one that names the status", msg)
		}
	}()
	cl.Follow(cl.Get("/me"))
}

func TestClientFollowPanicsOnALocationItCannotRead(t *testing.T) {
	cl := routertest.NewClient(t, clientHandler())
	defer func() {
		if msg, _ := recover().(string); !strings.Contains(msg, "cannot read") {
			t.Errorf("panic = %q", msg)
		}
	}()
	cl.Follow(cl.Get("/go?to=http://%5B::1"))
}

func TestClientFollowOnARecordedResponse(t *testing.T) {
	c, rec := routertest.NewContext(t, newContext)
	if err := c.Redirect(http.StatusSeeOther, "/method"); err != nil {
		t.Fatal(err)
	}
	cl := routertest.NewClient(t, clientHandler(), routertest.Host("app.example.test"))
	cl.Follow(routertest.Recorded(rec)).Expect(t).Body("GET /method app.example.test http")
}

func TestClientIsSafeForConcurrentUse(t *testing.T) {
	cl := routertest.NewClient(t, clientHandler())
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			cl.Get("/host")
			cl.Get("/")
		})
	}
	wg.Wait()
	if cl.Cookie("hostonly") == nil {
		t.Error("the jar lost the cookie")
	}
}
