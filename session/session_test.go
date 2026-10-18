package session_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/cookie"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/session"
)

type Data struct {
	UserID string `json:"uid"`
	Cart   []string
}

type appContext struct {
	router.Base
}

func newRouter() *router.Router[*appContext] {
	return router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
}

// routes mounts the handlers every test drives: /show reads the session,
// /login renews it and puts the user, /cart adds to it, and /logout ends it.
func routes(r *router.Router[*appContext]) {
	r.GET("/show", func(c *appContext) error {
		d, ok := session.From[Data](c)
		if !ok {
			return c.String(http.StatusOK, "none")
		}
		return c.String(http.StatusOK, d.UserID+"|"+strings.Join(d.Cart, ","))
	})
	r.POST("/login", func(c *appContext) error {
		if err := session.Renew(c); err != nil {
			return err
		}
		d, _ := session.From[Data](c)
		d.UserID = c.Request().URL.Query().Get("as")
		if err := session.Put(c, d); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
	r.POST("/cart", func(c *appContext) error {
		d, _ := session.From[Data](c)
		d.Cart = append(d.Cart, c.Request().URL.Query().Get("item"))
		if err := session.Put(c, d); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
	r.POST("/logout", func(c *appContext) error {
		if err := session.Destroy(c); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
}

func newApp(t *testing.T, cfg session.Config[*appContext, Data]) *routertest.Client {
	t.Helper()
	r := newRouter()
	r.Use(session.MiddlewareWithConfig(cfg))
	routes(r)
	return routertest.NewClient(t, r)
}

func mustPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Errorf("did not panic, want a panic containing %q", want)
			return
		}
		if msg, _ := r.(string); !strings.Contains(msg, want) {
			t.Errorf("panic = %v, want it to contain %q", r, want)
		}
	}()
	f()
}

func sessionToken(t *testing.T, c *routertest.Client) string {
	t.Helper()
	if ck := c.Cookie(session.DefaultCookie); ck != nil {
		return ck.Value
	}
	return ""
}

func TestASessionAcrossRequests(t *testing.T) {
	store := session.NewMemoryStore()
	c := newApp(t, session.Config[*appContext, Data]{Store: store})

	c.Get("/show").Expect(t).Status(http.StatusOK).Body("none").Header("Vary", "Cookie")
	if store.Len() != 0 {
		t.Errorf("a visitor who stored nothing has %d records, want 0", store.Len())
	}

	res := c.Do(http.MethodPost, "/cart?item=tea")
	res.Expect(t).Status(http.StatusNoContent)
	ck := res.Cookies()
	if len(ck) != 1 || ck[0].Name != "session" || !ck[0].HttpOnly || ck[0].SameSite != http.SameSiteLaxMode || ck[0].Path != "/" {
		t.Fatalf("Set-Cookie = %v, want one HttpOnly, Lax session cookie at /", ck)
	}
	if ck[0].MaxAge <= 0 || ck[0].MaxAge > int(session.DefaultLifetime/time.Second) {
		t.Errorf("MaxAge = %d, want it to end with the session", ck[0].MaxAge)
	}

	c.Do(http.MethodPost, "/cart?item=jam").Expect(t).Status(http.StatusNoContent)
	c.Get("/show").Expect(t).Body("|tea,jam")
	if store.Len() != 1 {
		t.Errorf("one visitor has %d records, want 1", store.Len())
	}
}

func TestTheHostCookieOverHTTPS(t *testing.T) {
	r := newRouter()
	r.Use(session.Middleware[*appContext, Data](session.NewMemoryStore()))
	routes(r)
	c := routertest.NewClient(t, r, routertest.Header("X-Forwarded-Proto", "https"))

	res := c.Do(http.MethodPost, "/login?as=ada")
	ck := res.Cookies()
	if len(ck) != 1 || ck[0].Name != session.DefaultHostCookie || !ck[0].Secure || ck[0].Path != "/" || ck[0].Domain != "" {
		t.Fatalf("Set-Cookie = %v, want one Secure __Host- cookie at / with no Domain", ck)
	}
	c.Get("/show").Expect(t).Body("ada|")

	// A cookie of the HTTP name means nothing over HTTPS, so a parent domain
	// that sets it cannot stand in front of the session.
	tossed := routertest.Get(r, "/show", routertest.Header("X-Forwarded-Proto", "https"),
		routertest.Cookie(&http.Cookie{Name: session.DefaultCookie, Value: c.Cookie(session.DefaultHostCookie).Value}))
	tossed.Expect(t).Body("none")
}

func TestPutKeepsTheTokenOfAServerStore(t *testing.T) {
	c := newApp(t, session.Config[*appContext, Data]{Store: session.NewMemoryStore()})
	c.Do(http.MethodPost, "/cart?item=tea")
	res := c.Do(http.MethodPost, "/cart?item=jam")
	if got := res.Header.Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("Set-Cookie = %v, want none: the token did not change", got)
	}
}

func TestRenewMovesTheSessionToANewToken(t *testing.T) {
	store := session.NewMemoryStore()
	c := newApp(t, session.Config[*appContext, Data]{Store: store})

	c.Do(http.MethodPost, "/cart?item=tea")
	planted := sessionToken(t, c)

	c.Do(http.MethodPost, "/login?as=ada").Expect(t).Status(http.StatusNoContent)
	if got := sessionToken(t, c); got == planted || got == "" {
		t.Fatalf("token after login = %q, want a new one", got)
	}
	c.Get("/show").Expect(t).Body("ada|tea")

	// The token from before the login names nothing now.
	if _, err := store.Load(context.Background(), planted); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("Load of the old token = %v, want ErrNotFound", err)
	}
	if store.Len() != 1 {
		t.Errorf("store holds %d records, want 1", store.Len())
	}
}

func TestRenewWithoutASessionStartsNone(t *testing.T) {
	store := session.NewMemoryStore()
	r := newRouter()
	r.Use(session.Middleware[*appContext, Data](store))
	r.POST("/", func(c *appContext) error {
		if err := session.Renew(c); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
	res := routertest.Do(r, http.MethodPost, "/")
	if len(res.Cookies()) != 0 || store.Len() != 0 {
		t.Errorf("Renew of no session set %v and stored %d records, want nothing", res.Cookies(), store.Len())
	}
}

func TestDestroyEndsTheSession(t *testing.T) {
	store := session.NewMemoryStore()
	c := newApp(t, session.Config[*appContext, Data]{Store: store})

	c.Do(http.MethodPost, "/login?as=ada")
	res := c.Do(http.MethodPost, "/logout")
	res.Expect(t).Status(http.StatusNoContent)
	if ck := res.Cookies(); len(ck) != 1 || ck[0].MaxAge >= 0 {
		t.Errorf("Set-Cookie = %v, want one that clears the cookie", ck)
	}
	if store.Len() != 0 {
		t.Errorf("store holds %d records after logout, want 0", store.Len())
	}
	c.Get("/show").Expect(t).Body("none")

	// A visitor with no cookie gets no Set-Cookie from a logout.
	if got := routertest.NewClient(t, c2(store)).Do(http.MethodPost, "/logout").Header.Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("Set-Cookie = %v, want none", got)
	}
}

func c2(store session.Store) http.Handler {
	r := newRouter()
	r.Use(session.Middleware[*appContext, Data](store))
	routes(r)
	return r
}

func TestPutKeepsTheOtherCookies(t *testing.T) {
	r := newRouter()
	r.Use(session.Middleware[*appContext, Data](session.NewMemoryStore()))
	r.POST("/", func(c *appContext) error {
		c.SetCookie(c.NewCookie("theme", "dark", time.Hour))
		if err := session.Put(c, Data{UserID: "a"}); err != nil {
			return err
		}
		if err := session.Put(c, Data{UserID: "b"}); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
	ck := routertest.Do(r, http.MethodPost, "/").Cookies()
	if len(ck) != 2 || ck[0].Name != "theme" || ck[1].Name != "session" {
		t.Errorf("Set-Cookie = %v, want theme, then one session cookie", ck)
	}
}

func TestDestroyThenPutStartsANewSession(t *testing.T) {
	store := session.NewMemoryStore()
	r := newRouter()
	r.Use(session.Middleware[*appContext, Data](store))
	routes(r)
	r.POST("/switch", func(c *appContext) error {
		if err := session.Destroy(c); err != nil {
			return err
		}
		if err := session.Put(c, Data{UserID: "grace"}); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
	c := routertest.NewClient(t, r)
	c.Do(http.MethodPost, "/login?as=ada")
	old := sessionToken(t, c)

	res := c.Do(http.MethodPost, "/switch")
	if got := res.Header.Values("Set-Cookie"); len(got) != 1 {
		t.Fatalf("Set-Cookie = %v, want the clear replaced by the new cookie", got)
	}
	if sessionToken(t, c) == old {
		t.Error("the new session kept the old token")
	}
	c.Get("/show").Expect(t).Body("grace|")
}

func TestAStoreOnTheServerRevokes(t *testing.T) {
	store := session.NewMemoryStore()
	c := newApp(t, session.Config[*appContext, Data]{Store: store})
	c.Do(http.MethodPost, "/login?as=ada")

	if err := store.Delete(context.Background(), sessionToken(t, c)); err != nil {
		t.Fatal(err)
	}
	res := c.Get("/show")
	res.Expect(t).Body("none")
	if ck := res.Cookies(); len(ck) != 1 || ck[0].MaxAge >= 0 {
		t.Errorf("Set-Cookie = %v, want the stale cookie cleared", ck)
	}
}

func TestAnUnknownCookieIsCleared(t *testing.T) {
	r := c2(session.NewMemoryStore())
	res := routertest.Get(r, "/show", routertest.Cookie(&http.Cookie{Name: "session", Value: "made-up"}))
	res.Expect(t).Body("none")
	if ck := res.Cookies(); len(ck) != 1 || ck[0].MaxAge >= 0 {
		t.Errorf("Set-Cookie = %v, want the cookie cleared", ck)
	}
	// An empty cookie is no session, and nothing to clear.
	res = routertest.Get(r, "/show", routertest.Cookie(&http.Cookie{Name: "session", Value: ""}))
	if len(res.Cookies()) != 0 {
		t.Errorf("Set-Cookie = %v, want none", res.Cookies())
	}
}

func TestARecordThatDoesNotDecodeIsNoSession(t *testing.T) {
	store := session.NewMemoryStore()
	if _, err := store.Save(context.Background(), "tok", []byte("not json"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	res := routertest.Get(c2(store), "/show", routertest.Cookie(&http.Cookie{Name: "session", Value: "tok"}))
	res.Expect(t).Body("none")
	if len(res.Cookies()) != 1 {
		t.Errorf("Set-Cookie = %v, want the cookie cleared", res.Cookies())
	}
}

func TestTheLifetimeEndsASession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newApp(t, session.Config[*appContext, Data]{Store: session.NewMemoryStore(), Lifetime: time.Hour})
		c.Do(http.MethodPost, "/login?as=ada")
		time.Sleep(59 * time.Minute)
		c.Get("/show").Expect(t).Body("ada|")
		time.Sleep(time.Minute)
		c.Get("/show").Expect(t).Body("none")
	})
}

func TestTheLifetimeEndsARecordTheStoreKept(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A store that keeps records forever, so the middleware checks the
		// expiry of the record itself.
		store := &foreverStore{MemoryStore: session.NewMemoryStore()}
		c := newApp(t, session.Config[*appContext, Data]{Store: store, Lifetime: time.Hour})
		c.Do(http.MethodPost, "/login?as=ada")
		tok := sessionToken(t, c)
		time.Sleep(2 * time.Hour)
		routertest.Get(c2(store), "/show", routertest.Cookie(&http.Cookie{Name: "session", Value: tok})).Expect(t).Body("none")
	})
}

type foreverStore struct {
	*session.MemoryStore
}

func (s *foreverStore) Save(ctx context.Context, token string, data []byte, _ time.Time) (string, error) {
	return s.MemoryStore.Save(ctx, token, data, time.Now().Add(1000*time.Hour))
}

func TestTheIdleTimeoutMovesForward(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := session.NewMemoryStore()
		c := newApp(t, session.Config[*appContext, Data]{Store: store, Lifetime: 3 * time.Hour, IdleTimeout: time.Hour})
		c.Do(http.MethodPost, "/login?as=ada")

		// Before half of the timeout, a request writes nothing.
		time.Sleep(20 * time.Minute)
		res := c.Get("/show")
		res.Expect(t).Body("ada|")
		if got := res.Header.Values("Set-Cookie"); len(got) != 0 {
			t.Errorf("Set-Cookie = %v, want none before half the timeout", got)
		}

		// After it, the end moves forward, and the cookie with it.
		time.Sleep(20 * time.Minute)
		res = c.Get("/show")
		if ck := res.Cookies(); len(ck) != 1 || ck[0].MaxAge != 3600 {
			t.Errorf("Set-Cookie = %v, want a MaxAge of one hour", ck)
		}

		// Steady use keeps the session up to the Lifetime, and no further.
		for range 5 {
			time.Sleep(40 * time.Minute)
			c.Get("/show")
		}
		c.Get("/show").Expect(t).Body("none")
	})
}

func TestTheIdleTimeoutEndsAQuietSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newApp(t, session.Config[*appContext, Data]{Store: session.NewMemoryStore(), IdleTimeout: time.Hour})
		c.Do(http.MethodPost, "/login?as=ada")
		time.Sleep(61 * time.Minute)
		c.Get("/show").Expect(t).Body("none")
	})
}

func TestACookieStore(t *testing.T) {
	codec := cookie.NewEncryptedCodec([]byte(strings.Repeat("k", cookie.KeyLen)))
	store := session.NewCookieStore(codec)
	c := newApp(t, session.Config[*appContext, Data]{Store: store, Cookie: "sid"})

	c.Do(http.MethodPost, "/cart?item=tea").Expect(t).Status(http.StatusNoContent)
	first := c.Cookie("sid")
	if first == nil || strings.Contains(first.Value, "tea") {
		t.Fatalf("cookie = %v, want a sealed record", first)
	}
	c.Do(http.MethodPost, "/login?as=ada")
	c.Get("/show").Expect(t).Body("ada|tea")

	// A cookie store cannot revoke: the copy from before still opens.
	routertest.Get(c2Named(store, "sid"), "/show", routertest.Cookie(first)).Expect(t).Body("|tea")

	// A record signed for another cookie of the same codec does not open.
	other := codec.Encode("other", []byte(`{"d":{"uid":"eve"},"c":0,"e":99999999999}`), time.Now().Add(time.Hour))
	routertest.Get(c2Named(store, "sid"), "/show", routertest.Cookie(&http.Cookie{Name: "sid", Value: other})).Expect(t).Body("none")

	// Nor does the session of one app moved to the cookie of another that
	// shares the codec.
	moved := &http.Cookie{Name: "admin_sid", Value: first.Value}
	routertest.Get(c2Named(store, "admin_sid"), "/show", routertest.Cookie(moved)).Expect(t).Body("none")
}

func TestCookieNameOutsideTheMiddleware(t *testing.T) {
	if got := session.CookieName(context.Background()); got != session.DefaultCookie {
		t.Errorf("CookieName = %q, want %q", got, session.DefaultCookie)
	}
}

func c2Named(store session.Store, name string) http.Handler {
	r := newRouter()
	r.Use(session.MiddlewareWithConfig(session.Config[*appContext, Data]{Store: store, Cookie: name}))
	routes(r)
	return r
}

func TestACookieStoreRefusesALargeValue(t *testing.T) {
	store := session.NewCookieStore(cookie.NewCodec([]byte(strings.Repeat("k", cookie.MinKeyLen))))
	var got error
	r := newRouter()
	r.Use(session.Middleware[*appContext, Data](store))
	r.POST("/", func(c *appContext) error {
		got = session.Put(c, Data{UserID: strings.Repeat("x", cookie.MaxSize)})
		return c.NoContent(http.StatusNoContent)
	})
	res := routertest.Do(r, http.MethodPost, "/")
	if !errors.Is(got, cookie.ErrTooLarge) {
		t.Errorf("Put = %v, want ErrTooLarge", got)
	}
	if len(res.Cookies()) != 0 {
		t.Errorf("Set-Cookie = %v, want none", res.Cookies())
	}
}

func TestErrCommitted(t *testing.T) {
	r := newRouter()
	r.Use(session.Middleware[*appContext, Data](session.NewMemoryStore()))
	var errs []error
	r.GET("/", func(c *appContext) error {
		if err := c.String(http.StatusOK, "streamed"); err != nil {
			return err
		}
		errs = append(errs, session.Put(c, Data{}), session.Renew(c), session.Destroy(c))
		return nil
	})
	routertest.Get(r, "/").Expect(t).Body("streamed")
	for i, err := range errs {
		if !errors.Is(err, session.ErrCommitted) {
			t.Errorf("call %d = %v, want ErrCommitted", i, err)
		}
	}
}

type failingStore struct {
	*session.MemoryStore
	load, save, del error
}

func (s *failingStore) Load(ctx context.Context, token string) ([]byte, error) {
	if s.load != nil {
		return nil, s.load
	}
	return s.MemoryStore.Load(ctx, token)
}

func (s *failingStore) Save(ctx context.Context, token string, data []byte, expires time.Time) (string, error) {
	if s.save != nil {
		return "", s.save
	}
	return s.MemoryStore.Save(ctx, token, data, expires)
}

func (s *failingStore) Delete(ctx context.Context, token string) error {
	if s.del != nil {
		return s.del
	}
	return s.MemoryStore.Delete(ctx, token)
}

func TestStoreErrors(t *testing.T) {
	down := errors.New("db down")
	store := &failingStore{MemoryStore: session.NewMemoryStore()}
	c := routertest.NewClient(t, c2(store))
	c.Do(http.MethodPost, "/login?as=ada")

	store.load = down
	c.Get("/show").Expect(t).Status(http.StatusInternalServerError)
	store.load = nil

	store.save = down
	c.Do(http.MethodPost, "/cart?item=tea").Expect(t).Status(http.StatusInternalServerError)
	store.save = nil

	store.del = down
	c.Do(http.MethodPost, "/login?as=grace").Expect(t).Status(http.StatusInternalServerError)
	c.Do(http.MethodPost, "/logout").Expect(t).Status(http.StatusInternalServerError)
	store.del = nil
	c.Get("/show").Expect(t).Body("ada|")
}

func TestAnIdleSaveErrorAnswers500(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &failingStore{MemoryStore: session.NewMemoryStore()}
		r := newRouter()
		r.Use(session.MiddlewareWithConfig(session.Config[*appContext, Data]{Store: store, IdleTimeout: time.Hour}))
		routes(r)
		c := routertest.NewClient(t, r)
		c.Do(http.MethodPost, "/login?as=ada")
		time.Sleep(45 * time.Minute)
		store.save = errors.New("db down")
		c.Get("/show").Expect(t).Status(http.StatusInternalServerError)
	})
}

func TestAValueThatDoesNotEncode(t *testing.T) {
	r := newRouter()
	r.Use(session.Middleware[*appContext, chan int](session.NewMemoryStore()))
	var got error
	r.POST("/", func(c *appContext) error {
		got = session.Put(c, make(chan int))
		return c.NoContent(http.StatusNoContent)
	})
	routertest.Do(r, http.MethodPost, "/")
	if got == nil || !strings.Contains(got.Error(), "encode") {
		t.Errorf("Put = %v, want an encode error", got)
	}
}

func TestSkipPassesWithoutASession(t *testing.T) {
	r := newRouter()
	r.Use(session.MiddlewareWithConfig(session.Config[*appContext, Data]{
		Store: &failingStore{MemoryStore: session.NewMemoryStore(), load: errors.New("never")},
		Skip:  func(c *appContext) bool { return true },
	}))
	routes(r)
	routertest.Get(r, "/show", routertest.Cookie(&http.Cookie{Name: "session", Value: "x"})).Expect(t).Status(http.StatusOK).Body("none")
}

func TestFromWithoutTheMiddleware(t *testing.T) {
	if _, ok := session.From[Data](context.Background()); ok {
		t.Error("From found a session on a bare context")
	}
}

func TestPanics(t *testing.T) {
	mustPanic(t, "needs a Store", func() { session.Middleware[*appContext, Data](nil) })
	mustPanic(t, "cannot name a cookie", func() {
		session.MiddlewareWithConfig(session.Config[*appContext, Data]{Store: session.NewMemoryStore(), Cookie: "a b"})
	})
	mustPanic(t, "of zero or more", func() {
		session.MiddlewareWithConfig(session.Config[*appContext, Data]{Store: session.NewMemoryStore(), IdleTimeout: -1})
	})
	mustPanic(t, "needs a codec", func() { session.NewCookieStore(nil) })
	mustPanic(t, "Put needs session.Middleware", func() { _ = session.Put(context.Background(), Data{}) })
	mustPanic(t, "Renew needs session.Middleware", func() { _ = session.Renew(context.Background()) })
	mustPanic(t, "Destroy needs session.Middleware", func() { _ = session.Destroy(context.Background()) })

	r := newRouter()
	r.Use(session.Middleware[*appContext, Data](session.NewMemoryStore()))
	r.GET("/", func(c *appContext) error {
		mustPanic(t, "Put[string] on a session of session_test.Data", func() { _ = session.Put(c, "x") })
		return nil
	})
	routertest.Get(r, "/")
}

func TestMemoryStoreDropsRecordsThatRanOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		m := session.NewMemoryStore()
		if _, err := m.Save(ctx, "old", []byte("x"), time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		if _, err := m.Load(ctx, "old"); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("Load = %v, want ErrNotFound for a record that ran out", err)
		}

		if _, err := m.Save(ctx, "old", []byte("x"), time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		for i := range 1023 {
			if _, err := m.Save(ctx, session.NewToken(), []byte{byte(i)}, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
		if m.Len() != 1023 {
			t.Errorf("Len = %d, want 1023 once the sweep dropped the old record", m.Len())
		}
	})
}

func TestNewTokenIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		tok := session.NewToken()
		if seen[tok] || len(tok) < 26 {
			t.Fatalf("NewToken = %q, want a fresh token of 26 characters or more", tok)
		}
		seen[tok] = true
	}
}
