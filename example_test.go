package router_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// Context is the request context of the application. It embeds router.Base,
// which is what makes it satisfy router.Context, and adds whatever the
// handlers need.
type Context struct {
	router.Base

	DB   *store
	User *User
}

// CurrentUser is an ordinary method on the application context, so a handler
// reads it without a type assertion.
func (c *Context) CurrentUser() *User { return c.User }

type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type store struct{ users map[string]*User }

func (s *store) find(id string) (*User, bool) { u, ok := s.users[id]; return u, ok }

func Example() {
	db := &store{users: map[string]*User{"7": {ID: "7", Name: "ann"}}}

	// The factory supplies the application fields. The router fills the
	// embedded Base with the request, the response and the route parameters.
	r := router.New(func(http.ResponseWriter, *http.Request) *Context {
		return &Context{DB: db}
	})
	r.Use(middleware.Recover, middleware.RequestID)

	r.GET("/users/{id}", func(c *Context) error {
		u, ok := c.DB.find(c.Param("id"))
		if !ok {
			return router.ErrNotFound.WithMessage("no user %s", c.Param("id"))
		}
		return c.JSON(http.StatusOK, u)
	})

	fmt.Println(serve(r, http.MethodGet, "/users/7"))
	fmt.Println(serve(r, http.MethodGet, "/users/9"))
	fmt.Println(serve(r, http.MethodDelete, "/users/7"))
	// Output:
	// 200 {"id":"7","name":"ann"}
	// 404 {"status":404,"error":"no user 9"}
	// 405 {"status":405,"error":"Method Not Allowed"}
}

// ExampleRouter_Route groups routes under a prefix and gives the group its own
// middleware.
func ExampleRouter_Route() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })

	requireAdmin := func(next router.HandlerFunc[*Context]) router.HandlerFunc[*Context] {
		return func(c *Context) error {
			if c.Request().Header.Get("X-Admin") == "" {
				return router.ErrForbidden
			}
			return next(c)
		}
	}

	r.GET("/health", func(c *Context) error { return c.String(http.StatusOK, "ok") })
	r.Route("/admin", func(g *router.Router[*Context]) {
		g.Use(requireAdmin)
		g.GET("/stats", func(c *Context) error { return c.String(http.StatusOK, "stats") })
	})

	fmt.Println(serve(r, http.MethodGet, "/health"))
	fmt.Println(serve(r, http.MethodGet, "/admin/stats"))
	// Output:
	// 200 ok
	// 403 {"status":403,"error":"Forbidden"}
}

// ExampleRouter_Mount attaches a router at a prefix. The routes of the mounted
// router join the trie of the parent, so a parameter of the prefix stays
// readable inside them.
func ExampleRouter_Mount() {
	newCtx := func(http.ResponseWriter, *http.Request) *Context { return new(Context) }

	api := router.New(newCtx)
	api.GET("/users/{id}", func(c *Context) error {
		return c.Stringf(http.StatusOK, "tenant=%s user=%s", c.Param("tenant"), c.Param("id"))
	})

	r := router.New(newCtx)
	r.Route("/t/{tenant}", func(g *router.Router[*Context]) {
		g.Mount("/api", api)
	})

	fmt.Println(serve(r, http.MethodGet, "/t/acme/api/users/7"))
	for _, rt := range r.Routes() {
		if rt.Method == http.MethodGet {
			fmt.Println(rt.Method, rt.Pattern)
		}
	}
	// Output:
	// 200 tenant=acme user=7
	// GET /t/{tenant}/api/users/{id}
}

// ExampleRouter_MountRouter attaches a router that carries a different context
// type. It serves the request on its own and sees the path with the prefix
// removed.
func ExampleRouter_MountRouter() {
	type AdminContext struct {
		router.Base
		Role string
	}

	admin := router.New(func(http.ResponseWriter, *http.Request) *AdminContext {
		return &AdminContext{Role: "root"}
	})
	admin.GET("/users/{id}", func(c *AdminContext) error {
		return c.Stringf(http.StatusOK, "%s sees user %s at %s", c.Role, c.Param("id"), c.Path())
	})

	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.MountRouter("/admin", admin)

	fmt.Println(serve(r, http.MethodGet, "/admin/users/7"))
	// Output:
	// 200 root sees user 7 at /users/7
}

// ExampleBase_Bind decodes a request body into a type that the caller names.
func ExampleBase_Bind() {
	type CreateUser struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.POST("/users", func(c *Context) error {
		in, err := c.Bind[CreateUser]()
		if err != nil {
			return err
		}
		return c.Stringf(http.StatusCreated, "%s is %d", in.Name, in.Age)
	})

	req := httptest.NewRequest(http.MethodPost, "/users",
		strings.NewReader(`{"name":"ann","age":30}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	fmt.Println(rec.Code, rec.Body.String())
	// Output:
	// 201 ann is 30
}

func serve(h http.Handler, method, target string) string {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return fmt.Sprint(rec.Code, " ", rec.Body.String())
}
