package router_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

type Context struct {
	router.Base

	DB   *store
	User *User
}

func (c *Context) CurrentUser() *User { return c.User }

type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type store struct{ users map[string]*User }

func (s *store) find(id string) (*User, bool) { u, ok := s.users[id]; return u, ok }

func Example() {
	db := &store{users: map[string]*User{"7": {ID: "7", Name: "ann"}}}

	r := router.New(func(http.ResponseWriter, *http.Request) *Context {
		return &Context{DB: db}
	})
	r.Use(middleware.Recover[*Context], middleware.RequestID[*Context])

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

func serveHost(h http.Handler, method, host, target string) string {
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return fmt.Sprint(rec.Code, " ", rec.Body.String())
}

func ExampleRouter_Host() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })

	r.Host("example.com", func(h *router.Router[*Context]) {
		h.GET("/", func(c *Context) error { return c.String(http.StatusOK, "landing") })
		h.Route("/blog", func(b *router.Router[*Context]) {
			b.GET("/{slug}", func(c *Context) error {
				return c.Stringf(http.StatusOK, "post %s", c.Param("slug"))
			})
		})
	})

	r.Host("api.example.com", func(h *router.Router[*Context]) {
		h.GET("/v1/users/{id}", func(c *Context) error {
			return c.Stringf(http.StatusOK, "user %s", c.Param("id"))
		})
	})

	r.Hosts([]string{"{tenant}.example.com", "*"}, func(h *router.Router[*Context]) {
		h.GET("/", func(c *Context) error {
			tenant := c.Param("tenant")
			if tenant == "" {
				tenant = "domain:" + c.Host()
			}
			return c.Stringf(http.StatusOK, "dashboard of %s", tenant)
		})
	})

	r.GET("/healthz", func(c *Context) error { return c.String(http.StatusOK, "ok") })

	fmt.Println(serveHost(r, http.MethodGet, "example.com", "/"))
	fmt.Println(serveHost(r, http.MethodGet, "example.com", "/blog/hello"))
	fmt.Println(serveHost(r, http.MethodGet, "api.example.com", "/v1/users/7"))
	fmt.Println(serveHost(r, http.MethodGet, "acme.example.com", "/"))
	fmt.Println(serveHost(r, http.MethodGet, "acme.com", "/"))
	fmt.Println(serveHost(r, http.MethodGet, "acme.com", "/healthz"))
	// Output:
	// 200 landing
	// 200 post hello
	// 200 user 7
	// 200 dashboard of acme
	// 200 dashboard of domain:acme.com
	// 200 ok
}

func ExampleRouter_HostRouter() {
	type APIContext struct {
		router.Base
		Version string
	}

	api := router.New(func(http.ResponseWriter, *http.Request) *APIContext {
		return &APIContext{Version: "v1"}
	})
	api.NotFound(func(c *APIContext) error {
		return c.Stringf(http.StatusNotFound, "%s: no such endpoint", c.Version)
	})
	api.GET("/users/{id}", func(c *APIContext) error {
		return c.Stringf(http.StatusOK, "%s user %s", c.Version, c.Param("id"))
	})

	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.HostRouter("api.example.com", api)
	r.Host("example.com", func(h *router.Router[*Context]) {
		h.GET("/", func(c *Context) error { return c.String(http.StatusOK, "landing") })
	})

	fmt.Println(serveHost(r, http.MethodGet, "api.example.com", "/users/7"))
	fmt.Println(serveHost(r, http.MethodGet, "api.example.com", "/nope"))
	fmt.Println(serveHost(r, http.MethodGet, "example.com", "/"))
	// Output:
	// 200 v1 user 7
	// 404 v1: no such endpoint
	// 200 landing
}

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

func TestReadmeContracts(t *testing.T) {
	newContext := func(http.ResponseWriter, *http.Request) *Context { return new(Context) }
	r := router.New(newContext)
	r.Use(middleware.Recover[*Context], middleware.Logger[*Context])
	sub := router.New(newContext)
	sub.GET("/users/{id}", func(c *Context) error { return c.NoContent(http.StatusOK) })
	r.Mount("/api", sub)
	req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	req.Header.Set(router.HeaderXForwardedProto, "HTTPS, http")
	if got := router.SchemeOf(req); got != "https" {
		t.Fatalf("SchemeOf() = %q", got)
	}
	req.Header.Set(router.HeaderXForwardedProto, "ftp")
	if got := router.SchemeOf(req); got != "http" {
		t.Fatalf("SchemeOf() = %q", got)
	}

	store := middleware.NewMemoryStoreWithConfig[*Context](middleware.MemoryStoreConfig{
		Rate:       10,
		Burst:      30,
		ExpiresIn:  time.Minute,
		MaxEntries: 1024,
	})
	if middleware.RateLimit(store) == nil {
		t.Fatal("RateLimit() returned nil")
	}
	if router.HTMXPartial(
		func(c *Context) error { return c.NoContent(http.StatusOK) },
		func(c *Context) error { return c.NoContent(http.StatusOK) },
	) == nil {
		t.Fatal("HTMXPartial() returned nil")
	}
}

func ExampleNewPooled() {
	r := router.NewPooled(
		func() *Context { return &Context{DB: &store{}} },
		func(c *Context) { c.User = nil },
	)

	r.GET("/whoami", func(c *Context) error {
		if c.User == nil {
			return c.String(http.StatusOK, "anonymous")
		}
		return c.String(http.StatusOK, c.User.Name)
	})
	r.GET("/login", func(c *Context) error {
		c.User = &User{ID: "7", Name: "ann"}
		return c.String(http.StatusOK, "signed in as "+c.User.Name)
	})

	fmt.Println(serve(r, http.MethodGet, "/login"))
	fmt.Println(serve(r, http.MethodGet, "/whoami"))
	// Output:
	// 200 signed in as ann
	// 200 anonymous
}

func ExampleRouter_GET_partialSegment() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })

	r.GET("/reports/rep-{date}.csv", func(c *Context) error {
		date, err := c.ParamAs[int]("date")
		if err != nil {
			return err
		}
		return c.Stringf(http.StatusOK, "report for %d", date)
	})
	r.GET("/files/{name}.{ext}", func(c *Context) error {
		return c.Stringf(http.StatusOK, "name=%s ext=%s", c.Param("name"), c.Param("ext"))
	})

	fmt.Println(serve(r, http.MethodGet, "/reports/rep-20260102.csv"))
	fmt.Println(serve(r, http.MethodGet, "/files/notes.v2.txt"))
	// Output:
	// 200 report for 20260102
	// 200 name=notes.v2 ext=txt
}

func page(title string) router.ComponentFunc {
	return func(ctx context.Context, w io.Writer) error {
		path := "unknown"
		if c, ok := router.FromContext(ctx); ok {
			path = c.Path()
		}
		_, err := fmt.Fprintf(w, "<h1>%s</h1><p>%s</p>", title, path)
		return err
	}
}

func ExampleBase_Render() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.GET("/posts/{slug}", func(c *Context) error {
		return c.Render(http.StatusOK, page(c.Param("slug")))
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/posts/hello", nil))
	fmt.Println(rec.Code, rec.Header().Get("Content-Type"))
	fmt.Println(rec.Body.String())
	// Output:
	// 200 text/html; charset=utf-8
	// <h1>hello</h1><p>/posts/hello</p>
}

func ExampleServeSSE() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })

	r.GET("/users/stream", func(c *Context) error {
		users := make(chan *User, 2)
		users <- &User{ID: "7", Name: "ann"}
		users <- &User{ID: "8", Name: "bob"}
		close(users)

		return router.ServeSSE(c, users, router.SSEJSON[*User]("user"),
			router.SSEHeartbeat(15*time.Second))
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/stream", nil))
	fmt.Println(rec.Code, rec.Header().Get("Content-Type"))
	fmt.Print(rec.Body.String())
	// Output:
	// 200 text/event-stream
	// event: user
	// data: {"id":"7","name":"ann"}
	//
	// event: user
	// data: {"id":"8","name":"bob"}
}

func ExampleNewSSEStream() {
	stream := router.NewSSEStream(
		router.SSEComponent("user", card),
		router.SSERetry(3*time.Second),
	)

	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.GET("/users/stream", func(c *Context) error {
		users := make(chan *User, 1)
		users <- &User{ID: "7", Name: "ann"}
		close(users)

		return stream.Serve(c, users)
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/stream", nil))
	fmt.Print(rec.Body.String())
	// Output:
	// retry: 3000
	//
	// event: user
	// data: <li id="user-7">ann</li>
}

func card(u *User) router.ComponentFunc {
	return func(_ context.Context, w io.Writer) error {
		_, err := fmt.Fprintf(w, "<li id=%q>%s</li>", "user-"+u.ID, u.Name)
		return err
	}
}

func ExampleBase_HX() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })

	r.PUT("/users/{id}", func(c *Context) error {
		u := &User{ID: c.Param("id"), Name: "ann"}
		return c.HX().
			Retarget("#user-"+u.ID).
			Reswap(router.HXSwapOuterHTML).
			Trigger("user-saved").
			Render(http.StatusOK, card(u))
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/users/7", nil))
	fmt.Println(rec.Code)
	fmt.Println(rec.Header().Get(router.HeaderHXRetarget), rec.Header().Get(router.HeaderHXReswap))
	fmt.Println(rec.Header().Get(router.HeaderHXTrigger))
	fmt.Println(rec.Body.String())
	// Output:
	// 200
	// #user-7 outerHTML
	// user-saved
	// <li id="user-7">ann</li>
}

func ExampleBase_HX_redirect() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.POST("/join", func(c *Context) error { return c.HX().Redirect("/chat") })

	for _, htmx := range []bool{true, false} {
		req := httptest.NewRequest(http.MethodPost, "/join", nil)
		if htmx {
			req.Header.Set(router.HeaderHXRequest, "true")
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		fmt.Printf("%d HX-Redirect=%q Location=%q\n", rec.Code,
			rec.Header().Get(router.HeaderHXRedirect),
			rec.Header().Get("Location"))
	}
	// Output:
	// 200 HX-Redirect="/chat" Location=""
	// 303 HX-Redirect="" Location="/chat"
}

func ExampleHTMXPartial() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })

	r.GET("/users", router.HTMXPartial(
		func(c *Context) error { return c.Render(http.StatusOK, card(&User{ID: "7", Name: "ann"})) },
		func(c *Context) error { return c.Render(http.StatusOK, page("users")) },
	))

	for _, htmx := range []bool{true, false} {
		req := httptest.NewRequest(http.MethodGet, "/users", nil)
		if htmx {
			req.Header.Set(router.HeaderHXRequest, "true")
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		fmt.Println(rec.Body.String())
	}
	// Output:
	// <li id="user-7">ann</li>
	// <h1>users</h1><p>/users</p>
}
