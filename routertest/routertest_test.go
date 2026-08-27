package routertest_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
)

type appContext struct {
	router.Base
}

type user struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func newRouter() *router.Router[*appContext] {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext {
		return new(appContext)
	})
	r.POST("/users", func(c *appContext) error {
		in, err := c.Bind[user]()
		if err != nil {
			return err
		}
		in.Age++
		return c.JSON(http.StatusCreated, in)
	})
	r.GET("/users/{id}", func(c *appContext) error {
		return c.String(http.StatusOK, "user "+c.Param("id"))
	})
	r.POST("/login", func(c *appContext) error {
		in, err := c.BindForm[user]()
		if err != nil {
			return err
		}
		c.SetHeader("X-Who", in.Name)
		return c.NoContent(http.StatusNoContent)
	})
	return r
}

func TestJSONRoundTrip(t *testing.T) {
	res := routertest.Do(newRouter(), http.MethodPost, "/users",
		routertest.JSONBody(user{Name: "ann", Age: 30}))
	res.AssertStatus(t, http.StatusCreated)

	got, err := res.JSON[user]()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != (user{Name: "ann", Age: 31}) {
		t.Errorf("got %+v", got)
	}
}

func TestGetAndBody(t *testing.T) {
	res := routertest.Get(newRouter(), "/users/7")
	res.AssertStatus(t, http.StatusOK)
	res.AssertBody(t, "user 7")
}

func TestFormBodyAndHeader(t *testing.T) {
	res := routertest.Do(newRouter(), http.MethodPost, "/login",
		routertest.FormBody(url.Values{"name": {"bo"}}))
	res.AssertStatus(t, http.StatusNoContent)
	res.AssertHeader(t, "X-Who", "bo")
}

func TestNewServer(t *testing.T) {
	srv := routertest.NewServer(t, newRouter())

	res, err := srv.Client().Get(srv.URL + "/users/9")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
}
