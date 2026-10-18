package user_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/user"
)

type Account struct {
	ID   string
	Name string
}

type appContext struct {
	router.Base
}

var accounts = map[string]Account{
	"u1": {ID: "u1", Name: "Ada"},
	"u2": {ID: "u2", Name: "Grace"},
}

var errSuspended = router.ErrForbidden.WithMessage("suspended")

// load reads the id from X-User, as a test stand-in for a session.
func load(c *appContext) (Account, error) {
	id := c.Request().Header.Get("X-User")
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

var users = user.New(load)

func as(id string) routertest.RequestOption { return routertest.Header("X-User", id) }

func newRouter() *router.Router[*appContext] {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.Use(users.Middleware())
	return r
}

// show answers with the name of the user, or "anonymous".
func show(c *appContext) error {
	a, ok := users.From(c)
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

func TestMiddlewareResolvesAndLetsAVisitorPass(t *testing.T) {
	r := newRouter()
	r.GET("/", show)

	routertest.Get(r, "/", as("u1")).Expect(t).Status(http.StatusOK).Body("Ada")
	routertest.Get(r, "/").Expect(t).Status(http.StatusOK).Body("anonymous")
	routertest.Get(r, "/", as("nobody")).Expect(t).Status(http.StatusOK).Body("anonymous")
	routertest.Get(r, "/", as("u3")).Expect(t).Status(http.StatusForbidden).Contains("suspended")
	routertest.Get(r, "/", as("broken")).Expect(t).Status(http.StatusInternalServerError)
}

func TestTheUserReachesEveryContext(t *testing.T) {
	r := newRouter()
	r.GET("/", func(c *appContext) error {
		a, ok := user.From[Account](c.Request().Context())
		if !ok {
			return c.String(http.StatusOK, "none")
		}
		return c.String(http.StatusOK, a.Name)
	})
	routertest.Get(r, "/", as("u2")).Expect(t).Body("Grace")
}

func TestRequireAnswers401(t *testing.T) {
	r := newRouter()
	r.With(users.Require(user.RequireConfig{})).GET("/me", show)

	routertest.Get(r, "/me").Expect(t).Status(http.StatusUnauthorized).Header("WWW-Authenticate", "Bearer")
	routertest.Get(r, "/me", as("u1")).Expect(t).Status(http.StatusOK).Body("Ada")
}

func TestRequireReportsErrMissing(t *testing.T) {
	var got error
	r := newRouter()
	r.ErrorHandler(func(c *appContext, err error) error {
		got = err
		return c.NoContent(router.StatusOf(err))
	})
	r.With(users.Require(user.RequireConfig{})).GET("/", show)
	routertest.Get(r, "/").Expect(t).Status(http.StatusUnauthorized)
	if !errors.Is(got, user.ErrMissing) {
		t.Errorf("err = %v, want it to wrap ErrMissing", got)
	}
}

func TestRequireTakesAChallenge(t *testing.T) {
	r := newRouter()
	r.With(users.Require(user.RequireConfig{Challenge: `Bearer realm="api"`})).GET("/api", show)
	r.With(func(next router.HandlerFunc[*appContext]) router.HandlerFunc[*appContext] {
		return func(c *appContext) error {
			c.Response().Header().Set("WWW-Authenticate", "Basic")
			return next(c)
		}
	}, users.Require(user.RequireConfig{})).GET("/set", show)

	routertest.Get(r, "/api").Expect(t).Status(http.StatusUnauthorized).Header("WWW-Authenticate", `Bearer realm="api"`)
	routertest.Get(r, "/set").Expect(t).Status(http.StatusUnauthorized).Header("WWW-Authenticate", "Basic")
}

func TestRequireSendsABrowserToTheLoginPage(t *testing.T) {
	r := newRouter()
	gate := users.Require(user.RequireConfig{LoginURL: "/login?via=gate"})
	r.With(gate).GET("/settings", show)
	r.With(gate).POST("/settings", show)

	routertest.Get(r, "/settings?tab=keys").Expect(t).
		Redirect(http.StatusSeeOther, "/login?next=%2Fsettings%3Ftab%3Dkeys&via=gate")
	// The login cannot repeat a POST, so it gets no next value.
	routertest.Do(r, http.MethodPost, "/settings").Expect(t).Redirect(http.StatusSeeOther, "/login?via=gate")
	routertest.Get(r, "/settings", as("u2")).Expect(t).Status(http.StatusOK).Body("Grace")
}

func TestRequireAnswersAJSONClientWith401(t *testing.T) {
	r := newRouter()
	r.With(users.Require(user.RequireConfig{LoginURL: "/login"})).GET("/settings", show)

	routertest.Get(r, "/settings", routertest.Header("Accept", "application/json")).
		Expect(t).Status(http.StatusUnauthorized)
	routertest.Get(r, "/settings", routertest.Header("Accept", "text/html,application/xhtml+xml")).
		Expect(t).Redirect(http.StatusSeeOther, "/login?next=%2Fsettings")
	// A tie goes to JSON, and a JSON ranked lower goes to HTML.
	routertest.Get(r, "/settings", routertest.Header("Accept", "text/html, application/json")).
		Expect(t).Status(http.StatusUnauthorized)
	routertest.Get(r, "/settings", routertest.Header("Accept", "text/html, application/json;q=0.5")).
		Expect(t).Redirect(http.StatusSeeOther, "/login?next=%2Fsettings")
	routertest.Get(r, "/settings", routertest.Header("Accept", "*/*")).
		Expect(t).Redirect(http.StatusSeeOther, "/login?next=%2Fsettings")
}

func TestRequireKeepsThePrefixOfAMount(t *testing.T) {
	inner := newRouter()
	inner.With(users.Require(user.RequireConfig{LoginURL: "/login"})).GET("/settings", show)

	r := newRouter()
	r.MountHandler("/admin", inner)
	routertest.Get(r, "/admin/settings?x=1").Expect(t).Redirect(http.StatusSeeOther, "/login?next=%2Fadmin%2Fsettings%3Fx%3D1")

	// A request built by hand has no RequestURI, and URL serves.
	req := routertest.Request(http.MethodGet, "/settings")
	req.RequestURI = ""
	routertest.Serve(inner, req).Expect(t).Redirect(http.StatusSeeOther, "/login?next=%2Fsettings")
}

func TestRequireSendsABrowserToALoginOnAnotherHost(t *testing.T) {
	r := newRouter()
	gate := users.Require(user.RequireConfig{LoginURL: "https://example.com/login"})
	r.With(gate).GET("/settings", show)
	r.With(gate).POST("/settings", show)

	routertest.Get(r, "https://acme.example.com/settings?tab=keys", routertest.Host("acme.example.com"),
		routertest.Header("X-Forwarded-Proto", "https")).Expect(t).
		Redirect(http.StatusSeeOther, "https://example.com/login?next=https%3A%2F%2Facme.example.com%2Fsettings%3Ftab%3Dkeys")
	routertest.Do(r, http.MethodPost, "/settings").Expect(t).Redirect(http.StatusSeeOther, "https://example.com/login")
}

func TestRequireTakesAnotherNextParam(t *testing.T) {
	r := newRouter()
	r.With(users.Require(user.RequireConfig{LoginURL: "/login", NextParam: "return_to"})).GET("/", show)
	routertest.Get(r, "/").Expect(t).Redirect(http.StatusSeeOther, "/login?return_to=%2F")
}

func TestNewContextForAJob(t *testing.T) {
	ctx := user.NewContext(context.Background(), accounts["u1"])
	if a, ok := users.From(ctx); !ok || a.Name != "Ada" {
		t.Errorf("From = %v, %v, want Ada", a, ok)
	}
	if _, ok := user.From[string](ctx); ok {
		t.Error("From[string] found a user of another type")
	}
}

func TestPanics(t *testing.T) {
	mustPanic(t, "needs a load function", func() { user.New[*appContext, Account](nil) })
	for _, bad := range []string{"login", "//apex.example/login", "ftp://apex.example/login", "https:///login"} {
		mustPanic(t, "a local path or an absolute http or https URL", func() {
			users.Require(user.RequireConfig{LoginURL: bad})
		})
	}
	mustPanic(t, "cannot use the LoginURL", func() {
		users.Require(user.RequireConfig{LoginURL: "/login#form"})
	})
	mustPanic(t, "cannot use the LoginURL", func() {
		users.Require(user.RequireConfig{LoginURL: "/log%zzin"})
	})
}
