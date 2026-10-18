package user_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/cookie"
	"github.com/dmitrymomot/go-router/internal/sessionfake"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/session"
	"github.com/dmitrymomot/go-router/user"
)

type Account struct {
	ID   string
	Name string
}

type appContext struct {
	router.Base
	KeyUser string
}

var accounts = map[string]Account{
	"u1": {ID: "u1", Name: "Ada"},
	"u2": {ID: "u2", Name: "Grace"},
}

var errSuspended = router.ErrForbidden.WithMessage("suspended")

func lookup(_ context.Context, id string) (Account, error) {
	switch id {
	case "u3":
		return Account{}, errSuspended
	case "broken":
		return Account{}, errors.New("db down")
	}
	if a, ok := accounts[id]; ok {
		return a, nil
	}
	return Account{}, user.ErrUnknown
}

var codec = cookie.NewCodec([]byte(strings.Repeat("k", cookie.MinKeyLen)))

// signed is the cookie that codec signs for id, as a login writes it.
func signed(name, id string) routertest.RequestOption {
	return routertest.Cookie(&http.Cookie{Name: name, Value: codec.Encode(name, []byte(id), time.Now().Add(time.Hour))})
}

func newRouter() *router.Router[*appContext] {
	return router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
}

// show answers with the name of the user, or "anonymous".
func show(c *appContext) error {
	a, ok := user.From[Account](c.Request().Context())
	if !ok {
		return c.String(http.StatusOK, "anonymous")
	}
	return c.String(http.StatusOK, a.Name)
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

func TestMiddlewareLetsAVisitorPass(t *testing.T) {
	r := newRouter()
	r.Use(user.Middleware[*appContext](lookup, user.FromCookie(codec, "uid")))
	r.GET("/", show)

	routertest.Get(r, "/", signed("uid", "u1")).Expect(t).Status(http.StatusOK).Body("Ada").Header("Vary", "Cookie")
	routertest.Get(r, "/").Expect(t).Status(http.StatusOK).Body("anonymous")
	routertest.Get(r, "/", signed("uid", "nobody")).Expect(t).Status(http.StatusOK).Body("anonymous")
	routertest.Get(r, "/", signed("uid", "u3")).Expect(t).Status(http.StatusForbidden).Contains("suspended")
	routertest.Get(r, "/", signed("uid", "broken")).Expect(t).Status(http.StatusInternalServerError)
}

func TestFromCookieRefusesAForgedCookie(t *testing.T) {
	r := newRouter()
	r.Use(user.Middleware[*appContext](lookup, user.FromCookie(codec, "uid")))
	r.GET("/", show)

	routertest.Get(r, "/", routertest.Cookie(&http.Cookie{Name: "uid", Value: "u1"})).Expect(t).Body("anonymous")
	// A value signed for another cookie does not move to this one.
	other := codec.Encode("other", []byte("u1"), time.Now().Add(time.Hour))
	routertest.Get(r, "/", routertest.Cookie(&http.Cookie{Name: "uid", Value: other})).Expect(t).Body("anonymous")
}

func TestSources(t *testing.T) {
	tests := []struct {
		name   string
		source user.Source
		opts   []routertest.RequestOption
		want   string
		vary   string
	}{
		{"cookie", user.FromCookie(codec, "uid"), []routertest.RequestOption{signed("uid", "u2")}, "Grace", "Cookie"},
		{"bearer", user.FromBearer(), []routertest.RequestOption{routertest.Header("Authorization", "Bearer u1")}, "Ada", "Authorization"},
		{"bearer in another case", user.FromBearer(), []routertest.RequestOption{routertest.Header("Authorization", "bearer  u2 ")}, "Grace", "Authorization"},
		{"basic is not bearer", user.FromBearer(), []routertest.RequestOption{routertest.Header("Authorization", "Basic u1")}, "anonymous", "Authorization"},
		{"bearer with no token", user.FromBearer(), []routertest.RequestOption{routertest.Header("Authorization", "Bearer ")}, "anonymous", "Authorization"},
		{"header", user.FromHeader("x-user-id"), []routertest.RequestOption{routertest.Header("X-User-Id", "u1")}, "Ada", "X-User-Id"},
		{"func", user.FromFunc("", func(c *appContext) (string, bool) { return "u2", true }), nil, "Grace", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRouter()
			r.Use(user.Middleware[*appContext](lookup, tt.source))
			r.GET("/", show)
			routertest.Get(r, "/", tt.opts...).Expect(t).Status(http.StatusOK).Body(tt.want).Header("Vary", tt.vary)
		})
	}
}

type sessionData struct {
	UserID string
}

func TestFromSession(t *testing.T) {
	r := newRouter()
	r.Use(session.Middleware[*appContext, sessionData](sessionfake.New()))
	r.Use(user.Middleware[*appContext](lookup, user.FromSession(func(d sessionData) string { return d.UserID }), user.FromBearer()))
	r.POST("/login", func(c *appContext) error {
		if err := session.Renew(c); err != nil {
			return err
		}
		return session.Put(c, sessionData{UserID: c.Request().URL.Query().Get("as")})
	})
	r.GET("/", show)

	c := routertest.NewClient(t, r)
	c.Get("/").Expect(t).Body("anonymous").Header("Vary", "Cookie")
	c.Do(http.MethodPost, "/login?as=")
	// A session with no user passes to the next source.
	c.Get("/", routertest.Header("Authorization", "Bearer u1")).Expect(t).Body("Ada")
	c.Do(http.MethodPost, "/login?as=u2")
	c.Get("/", routertest.Header("Authorization", "Bearer u1")).Expect(t).Body("Grace")
}

func TestTheFirstKnownSourceWins(t *testing.T) {
	var asked []string
	r := newRouter()
	r.Use(user.MiddlewareWithConfig(user.Config[*appContext, Account]{
		Lookup: func(ctx context.Context, id string) (Account, error) {
			asked = append(asked, id)
			return lookup(ctx, id)
		},
		Sources: []user.Source{
			user.FromFunc("X-Key", func(c *appContext) (string, bool) { return c.KeyUser, c.KeyUser != "" }),
			user.FromHeader("X-A"),
			user.FromBearer(),
			user.FromHeader("X-A"),
		},
	}))
	r.GET("/", show)
	res := routertest.Get(r, "/", routertest.Header("X-A", "nobody"), routertest.Header("Authorization", "Bearer u2"))
	res.Expect(t).Status(http.StatusOK).Body("Grace")
	if strings.Join(asked, ",") != "nobody,u2" {
		t.Errorf("asked %v, want nobody then u2", asked)
	}
	if got := res.Header.Values("Vary"); strings.Join(got, ",") != "X-Key,X-A,Authorization" {
		t.Errorf("Vary = %v, want every header source once", got)
	}
}

func TestMiddlewareWithConfigNeverRefuses(t *testing.T) {
	r := newRouter()
	r.Use(user.MiddlewareWithConfig(user.Config[*appContext, Account]{
		Lookup:  lookup,
		Sources: []user.Source{user.FromBearer()},
	}))
	r.GET("/", show)
	routertest.Get(r, "/").Expect(t).Status(http.StatusOK).Body("anonymous")
	routertest.Get(r, "/", routertest.Header("Authorization", "Bearer nobody")).Expect(t).Status(http.StatusOK).Body("anonymous")
	routertest.Get(r, "/", routertest.Header("Authorization", "Bearer u1")).Expect(t).Status(http.StatusOK).Body("Ada")
}

func TestRequireReportsErrMissing(t *testing.T) {
	var got error
	r := newRouter()
	r.ErrorHandler(func(c *appContext, err error) error {
		got = err
		return c.NoContent(router.StatusOf(err))
	})
	r.Use(user.Middleware[*appContext](lookup, user.FromBearer()), user.Require[*appContext, Account]())
	r.GET("/", show)
	routertest.Get(r, "/").Expect(t).Status(http.StatusUnauthorized)
	if !errors.Is(got, user.ErrMissing) {
		t.Errorf("err = %v, want it to wrap ErrMissing", got)
	}
}

func TestSkipPassesWithoutAUser(t *testing.T) {
	r := newRouter()
	r.Use(user.MiddlewareWithConfig(user.Config[*appContext, Account]{
		Lookup:  func(context.Context, string) (Account, error) { panic("Lookup ran for a skipped request") },
		Sources: []user.Source{user.FromBearer()},
		Skip:    func(c *appContext) bool { return c.Request().URL.Path == "/health" },
	}))
	r.GET("/health", show)
	routertest.Get(r, "/health", routertest.Header("Authorization", "Bearer u1")).Expect(t).Status(http.StatusOK).Body("anonymous")
}

func TestRequireAnswers401(t *testing.T) {
	r := newRouter()
	r.Use(user.Middleware[*appContext](lookup, user.FromBearer()))
	r.Route("/api", func(api *router.Router[*appContext]) {
		api.Use(user.Require[*appContext, Account]())
		api.GET("/me", show)
	})
	r.GET("/", show)

	routertest.Get(r, "/").Expect(t).Status(http.StatusOK).Body("anonymous")
	routertest.Get(r, "/api/me").Expect(t).Status(http.StatusUnauthorized).Header("WWW-Authenticate", "Bearer")
	routertest.Get(r, "/api/me", routertest.Header("Authorization", "Bearer u1")).Expect(t).Status(http.StatusOK).Body("Ada")
}

func TestRequireSendsABrowserToTheLoginPage(t *testing.T) {
	r := newRouter()
	r.Use(user.Middleware[*appContext](lookup, user.FromCookie(codec, "uid")))
	r.Route("/app", func(app *router.Router[*appContext]) {
		app.Use(user.RequireWithConfig[*appContext, Account](user.RequireConfig{LoginURL: "/login?via=gate"}))
		app.GET("/settings", show)
		app.POST("/settings", show)
	})

	routertest.Get(r, "/app/settings?tab=keys").Expect(t).
		Redirect(http.StatusSeeOther, "/login?next=%2Fapp%2Fsettings%3Ftab%3Dkeys&via=gate")
	// The login cannot repeat a POST, so it gets no next value.
	routertest.Do(r, http.MethodPost, "/app/settings").Expect(t).Redirect(http.StatusSeeOther, "/login?via=gate")
	routertest.Get(r, "/app/settings", signed("uid", "u2")).Expect(t).Status(http.StatusOK).Body("Grace")
}

func TestRequireKeepsThePrefixOfAMount(t *testing.T) {
	inner := newRouter()
	inner.Use(user.Middleware[*appContext](lookup, user.FromBearer()))
	inner.Use(user.RequireWithConfig[*appContext, Account](user.RequireConfig{LoginURL: "/login"}))
	inner.GET("/settings", show)

	r := newRouter()
	r.MountHandler("/admin", inner)
	routertest.Get(r, "/admin/settings?x=1").Expect(t).Redirect(http.StatusSeeOther, "/login?next=%2Fadmin%2Fsettings%3Fx%3D1")

	// A request built by hand has no RequestURI, and URL serves.
	req := routertest.Request(http.MethodGet, "/settings")
	req.RequestURI = ""
	routertest.Serve(inner, req).Expect(t).Redirect(http.StatusSeeOther, "/login?next=%2Fsettings")
}

func TestRequireOfAnotherTypePanics(t *testing.T) {
	var got error
	r := newRouter()
	r.ErrorHandler(func(c *appContext, err error) error {
		got = err
		return c.NoContent(router.StatusOf(err))
	})
	r.Use(user.Middleware[*appContext](lookup, user.FromBearer()))
	r.Use(user.Require[*appContext, *Account]())
	r.GET("/", show)
	// The router turns the panic into a 500 that carries its message.
	routertest.Get(r, "/").Expect(t).Status(http.StatusInternalServerError)
	if want := "Require[*user_test.Account] behind a user middleware for user_test.Account"; got == nil || !strings.Contains(got.Error(), want) {
		t.Errorf("err = %v, want it to name %q", got, want)
	}
}

func TestRequireWithoutAMiddlewareRefuses(t *testing.T) {
	r := newRouter()
	r.Use(user.Require[*appContext, Account]())
	r.GET("/", show)
	routertest.Get(r, "/").Expect(t).Status(http.StatusUnauthorized)
}

func TestRequireFindsTheTypeOfAnOuterMiddleware(t *testing.T) {
	r := newRouter()
	r.Use(user.Middleware[*appContext](lookup, user.FromBearer()))
	r.Use(user.Middleware[*appContext](func(context.Context, string) (string, error) { return "", user.ErrUnknown }, user.FromHeader("X-Key")))
	r.Use(user.Require[*appContext, Account]())
	r.GET("/", show)
	routertest.Get(r, "/").Expect(t).Status(http.StatusUnauthorized)
}

func TestRequireTakesAChallenge(t *testing.T) {
	r := newRouter()
	r.Use(user.Middleware[*appContext](lookup, user.FromBearer()))
	r.Route("/api", func(api *router.Router[*appContext]) {
		api.Use(user.RequireWithConfig[*appContext, Account](user.RequireConfig{Challenge: `Bearer realm="api"`}))
		api.GET("/me", show)
	})
	r.Route("/set", func(api *router.Router[*appContext]) {
		api.Use(func(next router.HandlerFunc[*appContext]) router.HandlerFunc[*appContext] {
			return func(c *appContext) error {
				c.Response().Header().Set("WWW-Authenticate", "Basic")
				return next(c)
			}
		})
		api.Use(user.Require[*appContext, Account]())
		api.GET("/me", show)
	})
	routertest.Get(r, "/api/me").Expect(t).Status(http.StatusUnauthorized).Header("WWW-Authenticate", `Bearer realm="api"`)
	routertest.Get(r, "/set/me").Expect(t).Status(http.StatusUnauthorized).Header("WWW-Authenticate", "Basic")
}

func TestRequireTakesAnotherNextParam(t *testing.T) {
	r := newRouter()
	r.Use(user.Middleware[*appContext](lookup, user.FromBearer()))
	r.Use(user.RequireWithConfig[*appContext, Account](user.RequireConfig{LoginURL: "/login", NextParam: "return_to"}))
	r.GET("/", show)
	routertest.Get(r, "/").Expect(t).Redirect(http.StatusSeeOther, "/login?return_to=%2F")
}

func TestNewContextForAJob(t *testing.T) {
	ctx := user.NewContext(context.Background(), accounts["u1"])
	if a, ok := user.From[Account](ctx); !ok || a.Name != "Ada" {
		t.Errorf("From = %v, %v, want Ada", a, ok)
	}
	if _, ok := user.From[string](ctx); ok {
		t.Error("From[string] found a user of another type")
	}
}

func TestPanics(t *testing.T) {
	mustPanic(t, "needs a codec", func() { user.FromCookie(nil, "uid") })
	mustPanic(t, "FromSession needs a function", func() { user.FromSession[sessionData](nil) })
	mustPanic(t, "FromCookie needs a name", func() { user.FromCookie(codec, "") })
	mustPanic(t, "FromHeader needs a name", func() { user.FromHeader("") })
	mustPanic(t, "FromFunc needs a function", func() { user.FromFunc[*appContext]("", nil) })
	mustPanic(t, "needs a Lookup", func() { user.Middleware[*appContext, Account](nil, user.FromBearer()) })
	mustPanic(t, "at least one Source", func() { user.Middleware[*appContext](lookup) })
	mustPanic(t, "the zero Source at index 1", func() {
		user.Middleware[*appContext](lookup, user.FromBearer(), user.Source{})
	})
	mustPanic(t, "got a FromFunc for", func() {
		user.Middleware[*appContext](lookup, user.FromFunc("", func(*router.Base) (string, bool) { return "", false }))
	})
	mustPanic(t, "a local path", func() {
		user.RequireWithConfig[*appContext, Account](user.RequireConfig{LoginURL: "https://evil.example/login"})
	})
	mustPanic(t, "cannot use the LoginURL", func() {
		user.RequireWithConfig[*appContext, Account](user.RequireConfig{LoginURL: "/login#form"})
	})
	mustPanic(t, "cannot use the LoginURL", func() {
		user.RequireWithConfig[*appContext, Account](user.RequireConfig{LoginURL: "/log%zzin"})
	})
}
