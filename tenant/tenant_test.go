package tenant_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/tenant"
)

type Workspace struct {
	ID   string
	Name string
}

type appContext struct {
	router.Base
	KeyTenant string
}

var workspaces = map[string]Workspace{
	"acme":   {ID: "acme", Name: "Acme"},
	"globex": {ID: "globex", Name: "Globex"},
}

var errSuspended = router.ErrNotFound.WithMessage("suspended")

func lookup(_ context.Context, id string) (Workspace, error) {
	switch id {
	case "initech":
		return Workspace{}, errSuspended
	case "broken":
		return Workspace{}, errors.New("db down")
	}
	if w, ok := workspaces[id]; ok {
		return w, nil
	}
	return Workspace{}, tenant.ErrUnknown
}

func newRouter() *router.Router[*appContext] {
	return router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
}

// show answers with the name of the tenant, or "none".
func show(c *appContext) error {
	w, ok := tenant.From[Workspace](c.Request().Context())
	if !ok {
		return c.String(http.StatusOK, "none")
	}
	return c.String(http.StatusOK, w.Name)
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

func TestMiddlewareReadsTheHostParameter(t *testing.T) {
	r := newRouter()
	r.Host("{tenant}.example.com", func(h *router.Router[*appContext]) {
		h.Use(tenant.Middleware[*appContext](lookup))
		h.GET("/", show)
	})
	routertest.Get(r, "/", routertest.Host("acme.example.com")).Expect(t).Status(http.StatusOK).Body("Acme")
	routertest.Get(r, "/", routertest.Host("nobody.example.com")).Expect(t).Status(http.StatusNotFound)
	routertest.Get(r, "/", routertest.Host("initech.example.com")).Expect(t).Status(http.StatusNotFound).Contains("suspended")
	routertest.Get(r, "/", routertest.Host("broken.example.com")).Expect(t).Status(http.StatusInternalServerError)
}

func TestSources(t *testing.T) {
	tests := []struct {
		name   string
		source tenant.Source
		target string
		opts   []routertest.RequestOption
		want   string
		vary   string
	}{
		{"path parameter", tenant.FromParam("ws"), "/t/globex", nil, "Globex", ""},
		{"host", tenant.FromHost(), "/t/x", []routertest.RequestOption{routertest.Host("ACME:8080")}, "Acme", ""},
		{"header", tenant.FromHeader("x-tenant-id"), "/t/x", []routertest.RequestOption{routertest.Header("X-Tenant-Id", "acme")}, "Acme", "X-Tenant-Id"},
		{"cookie", tenant.FromCookie("ws"), "/t/x", []routertest.RequestOption{routertest.Cookie(&http.Cookie{Name: "ws", Value: "globex"})}, "Globex", "Cookie"},
		{"func", tenant.FromFunc("", func(c *appContext) (string, bool) { return "acme", true }), "/t/x", nil, "Acme", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRouter()
			r.Use(tenant.MiddlewareWithConfig(tenant.Config[*appContext, Workspace]{
				Lookup:  lookup,
				Sources: []tenant.Source{tt.source},
			}))
			r.GET("/t/{ws}", show)
			e := routertest.Get(r, tt.target, tt.opts...).Expect(t).Status(http.StatusOK).Body(tt.want)
			e.Header("Vary", tt.vary)
		})
	}
}

func TestEmptySourcesFindNothing(t *testing.T) {
	r := newRouter()
	r.Use(tenant.MiddlewareWithConfig(tenant.Config[*appContext, Workspace]{
		Lookup: lookup,
		Sources: []tenant.Source{
			tenant.FromParam("missing"),
			tenant.FromHost(),
			tenant.FromHeader("X-Tenant"),
			tenant.FromCookie("ws"),
		},
		Optional: true,
	}))
	r.GET("/", show)
	routertest.Get(r, "/", routertest.Host(""),
		routertest.Header("X-Tenant", ""),
		routertest.Cookie(&http.Cookie{Name: "ws", Value: ""}),
	).Expect(t).Status(http.StatusOK).Body("none").Header("Vary", "X-Tenant")
}

func TestTheFirstKnownSourceWins(t *testing.T) {
	var asked []string
	r := newRouter()
	r.Use(tenant.MiddlewareWithConfig(tenant.Config[*appContext, Workspace]{
		Lookup: func(ctx context.Context, id string) (Workspace, error) {
			asked = append(asked, id)
			return lookup(ctx, id)
		},
		Sources: []tenant.Source{tenant.FromHeader("X-A"), tenant.FromHeader("X-B"), tenant.FromHeader("X-C")},
	}))
	r.GET("/", show)
	res := routertest.Get(r, "/", routertest.Header("X-A", "nobody"), routertest.Header("X-B", "globex"), routertest.Header("X-C", "acme"))
	res.Expect(t).Status(http.StatusOK).Body("Globex")
	if strings.Join(asked, ",") != "nobody,globex" {
		t.Errorf("asked %v, want nobody then globex", asked)
	}
	if got := res.Header.Values("Vary"); strings.Join(got, ",") != "X-A,X-B,X-C" {
		t.Errorf("Vary = %v, want every header source", got)
	}
}

func TestALookupErrorStopsTheChain(t *testing.T) {
	r := newRouter()
	r.Use(tenant.MiddlewareWithConfig(tenant.Config[*appContext, Workspace]{
		Lookup:  lookup,
		Sources: []tenant.Source{tenant.FromHeader("X-A"), tenant.FromHeader("X-B")},
	}))
	r.GET("/", show)
	routertest.Get(r, "/", routertest.Header("X-A", "initech"), routertest.Header("X-B", "acme")).
		Expect(t).Status(http.StatusNotFound).Contains("suspended")
	routertest.Get(r, "/", routertest.Header("X-A", "broken"), routertest.Header("X-B", "acme")).
		Expect(t).Status(http.StatusInternalServerError)
}

func TestTheCauseOfTheRefusal(t *testing.T) {
	var got error
	r := newRouter()
	r.Use(tenant.MiddlewareWithConfig(tenant.Config[*appContext, Workspace]{
		Lookup:  lookup,
		Sources: []tenant.Source{tenant.FromHeader("X-Tenant")},
		OnReject: func(c *appContext, err error) error {
			got = err
			return c.String(http.StatusTeapot, "pick a workspace")
		},
	}))
	r.GET("/", show)

	routertest.Get(r, "/").Expect(t).Status(http.StatusTeapot).Body("pick a workspace")
	if !errors.Is(got, tenant.ErrMissing) || router.StatusOf(got) != http.StatusNotFound {
		t.Errorf("err = %v, want a 404 wrapping ErrMissing", got)
	}
	routertest.Get(r, "/", routertest.Header("X-Tenant", "nobody")).Expect(t).Status(http.StatusTeapot)
	if !errors.Is(got, tenant.ErrUnknown) || router.StatusOf(got) != http.StatusNotFound {
		t.Errorf("err = %v, want a 404 wrapping ErrUnknown", got)
	}
}

func TestOptionalPassesWithoutATenant(t *testing.T) {
	r := newRouter()
	r.Use(tenant.MiddlewareWithConfig(tenant.Config[*appContext, Workspace]{
		Lookup:   lookup,
		Sources:  []tenant.Source{tenant.FromHeader("X-Tenant")},
		Optional: true,
	}))
	r.GET("/", show)
	routertest.Get(r, "/").Expect(t).Status(http.StatusOK).Body("none")
	routertest.Get(r, "/", routertest.Header("X-Tenant", "nobody")).Expect(t).Status(http.StatusOK).Body("none")
	routertest.Get(r, "/", routertest.Header("X-Tenant", "acme")).Expect(t).Status(http.StatusOK).Body("Acme")
}

func TestSkipPassesWithoutALookup(t *testing.T) {
	var calls atomic.Int32
	r := newRouter()
	r.Use(tenant.MiddlewareWithConfig(tenant.Config[*appContext, Workspace]{
		Skip: func(c *appContext) bool { return c.Path() == "/health" },
		Lookup: func(ctx context.Context, id string) (Workspace, error) {
			calls.Add(1)
			return lookup(ctx, id)
		},
	}))
	r.GET("/health", show)
	routertest.Get(r, "/health").Expect(t).Status(http.StatusOK).Body("none")
	if calls.Load() != 0 {
		t.Error("Skip still ran the Lookup")
	}
}

func TestFromReadsEveryContextOfTheRequest(t *testing.T) {
	r := newRouter()
	r.Use(tenant.MiddlewareWithConfig(tenant.Config[*appContext, Workspace]{
		Lookup:  lookup,
		Sources: []tenant.Source{tenant.FromFunc("", func(c *appContext) (string, bool) { return c.KeyTenant, c.KeyTenant != "" })},
	}))
	r.GET("/", func(c *appContext) error {
		a, okA := tenant.From[Workspace](c)
		b, okB := tenant.From[Workspace](context.WithoutCancel(c))
		if !okA || !okB || a != b {
			return c.String(http.StatusInternalServerError, "mismatch")
		}
		return c.String(http.StatusOK, a.ID)
	})
	r.Pre(func(next router.HandlerFunc[*appContext]) router.HandlerFunc[*appContext] {
		return func(c *appContext) error {
			c.KeyTenant = "globex"
			return next(c)
		}
	})
	routertest.Get(r, "/").Expect(t).Status(http.StatusOK).Body("globex")
}

func TestNewContextKeepsOneTenantPerType(t *testing.T) {
	type ID string
	ctx := tenant.NewContext(context.Background(), Workspace{ID: "acme"})
	ctx = tenant.NewContext(ctx, ID("other"))
	if w, ok := tenant.From[Workspace](ctx); !ok || w.ID != "acme" {
		t.Errorf("From[Workspace] = %v, %v; want acme", w, ok)
	}
	if id, ok := tenant.From[ID](ctx); !ok || id != "other" {
		t.Errorf("From[ID] = %v, %v; want other", id, ok)
	}
	if _, ok := tenant.From[*Workspace](ctx); ok {
		t.Error("From[*Workspace] found a tenant nobody put there")
	}
}

func TestBadConfigPanics(t *testing.T) {
	type otherContext struct{ router.Base }
	mustPanic(t, "tenant: FromParam needs a name", func() { tenant.FromParam("") })
	mustPanic(t, "tenant: FromHeader needs a name", func() { tenant.FromHeader("") })
	mustPanic(t, "tenant: FromCookie needs a name", func() { tenant.FromCookie("") })
	mustPanic(t, "tenant: FromFunc needs a function", func() { tenant.FromFunc[*appContext]("", nil) })
	mustPanic(t, "tenant: MiddlewareWithConfig needs a Lookup", func() {
		tenant.Middleware[*appContext, Workspace](nil)
	})
	mustPanic(t, "tenant: MiddlewareWithConfig got the zero Source at index 1", func() {
		tenant.MiddlewareWithConfig(tenant.Config[*appContext, Workspace]{
			Lookup: lookup, Sources: []tenant.Source{tenant.FromHost(), {}},
		})
	})
	mustPanic(t, "got a FromFunc for *tenant_test.otherContext at index 0", func() {
		tenant.MiddlewareWithConfig(tenant.Config[*appContext, Workspace]{
			Lookup:  lookup,
			Sources: []tenant.Source{tenant.FromFunc("X-Key", func(*otherContext) (string, bool) { return "", false })},
		})
	})
}
