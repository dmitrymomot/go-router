package router_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

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

// serveHost sends a request that names a host.
func serveHost(h http.Handler, method, host, target string) string {
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return fmt.Sprint(rec.Code, " ", rec.Body.String())
}

// ExampleRouter_Host routes a whole service by host: a main site, a fixed
// subdomain for the API, a subdomain per tenant, and the domain that a tenant
// brings along.
func ExampleRouter_Host() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })

	// The main site.
	r.Host("example.com", func(h *router.Router[*Context]) {
		h.GET("/", func(c *Context) error { return c.String(http.StatusOK, "landing") })
		h.Route("/blog", func(b *router.Router[*Context]) {
			b.GET("/{slug}", func(c *Context) error {
				return c.Stringf(http.StatusOK, "post %s", c.Param("slug"))
			})
		})
	})

	// A fixed subdomain, with middleware of its own.
	r.Host("api.example.com", func(h *router.Router[*Context]) {
		h.GET("/v1/users/{id}", func(c *Context) error {
			return c.Stringf(http.StatusOK, "user %s", c.Param("id"))
		})
	})

	// One tenant application, on a subdomain or on a domain of the tenant's
	// own. The subdomain fills the parameter; the custom domain does not, so
	// the handler falls back to the host itself.
	r.Hosts([]string{"{tenant}.example.com", "*"}, func(h *router.Router[*Context]) {
		h.GET("/", func(c *Context) error {
			tenant := c.Param("tenant")
			if tenant == "" {
				tenant = "domain:" + c.Host()
			}
			return c.Stringf(http.StatusOK, "dashboard of %s", tenant)
		})
	})

	// A route outside every host scope answers on any host.
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

// ExampleRouter_HostRouter gives a host to a router that carries a different
// context type, with its own middleware, error handler and fallbacks.
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

// ExampleNewPooled reuses contexts across requests. The factory takes no
// request, and reset clears every field that a handler writes.
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
	// The next request reuses that context, and reset cleared the user.
	fmt.Println(serve(r, http.MethodGet, "/whoami"))
	// Output:
	// 200 signed in as ann
	// 200 anonymous
}

// ExampleRouter_GET_partialSegment reads part of a path segment.
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

// page stands for a template that the a-h/templ generator produces. A
// generated templ.Component has exactly this shape, so it satisfies
// router.Component and needs no adapter.
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

// ExampleBase_Render writes an HTML body from a template component. The
// component reads the request through router.FromContext.
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

// ExampleServeSSE streams the values of a channel to the client as
// server-sent events, each one encoded as JSON.
//
// The channel of the example is closed, so the stream ends and the handler
// returns. A real one stays open until the client goes away, and the handler
// blocks in ServeSSE for as long as that takes.
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

// ExampleNewSSEStream declares the shape of a stream once, and every request
// of the route reuses it. The events carry HTML here, which is what an htmx
// page reads with its sse extension.
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

// card stands for the template that renders one user, the way page stands for
// a whole page.
func card(u *User) router.ComponentFunc {
	return func(_ context.Context, w io.Writer) error {
		_, err := fmt.Fprintf(w, "<li id=%q>%s</li>", "user-"+u.ID, u.Name)
		return err
	}
}

// ExampleBase_HX writes the htmx headers of an answer. Each method sets one
// header and returns the chain; the last one writes the body.
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

// ExampleBase_HX_redirect sends the browser to another page. htmx would follow
// a 303 inside the request that it made and swap the answer, so the handler
// asks for a client-side redirect instead. A client that is not htmx still
// gets the 303.
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

// ExampleHTMXPartial gives one URL two answers: the fragment that htmx swaps,
// and the page that a browser navigates to.
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
