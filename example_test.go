package router_test

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
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
	// 404 no user 9
	// 405 Method Not Allowed
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
	// 403 Forbidden
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
	api.ErrorHandler(func(c *APIContext, err error) error {
		return c.Stringf(router.StatusOf(err), "%s: no such endpoint", c.Version)
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

type localeKey struct{}

// localeOf stands in for domain code that takes a context.Context.
func localeOf(ctx context.Context) string {
	if lang, ok := ctx.Value(localeKey{}).(string); ok {
		return lang
	}
	return "en"
}

func ExampleBase_SetContext() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.Use(func(next router.HandlerFunc[*Context]) router.HandlerFunc[*Context] {
		return func(c *Context) error {
			if lang := c.Query("lang"); lang != "" {
				// Derive from Request().Context(), never from c.
				c.SetContext(context.WithValue(c.Request().Context(), localeKey{}, lang))
			}
			return next(c)
		}
	})
	r.GET("/", func(c *Context) error {
		return c.String(http.StatusOK, localeOf(c))
	})

	fmt.Println(serve(r, http.MethodGet, "/"))
	fmt.Println(serve(r, http.MethodGet, "/?lang=uk"))
	// Output:
	// 200 en
	// 200 uk
}

func ExampleBase_SetBodyLimit() {
	type Note struct {
		Text string `json:"text"`
	}

	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.MaxBodyBytes(16)
	r.POST("/import", func(c *Context) error {
		c.SetBodyLimit(1 << 10)
		if _, err := c.Bind[Note](); err != nil {
			return err
		}
		return c.NoContent(http.StatusOK)
	})
	r.POST("/note", func(c *Context) error {
		if _, err := c.Bind[Note](); err != nil {
			return err
		}
		return c.NoContent(http.StatusOK)
	})

	body := `{"text":"` + strings.Repeat("x", 30) + `"}`
	for _, target := range []string{"/import", "/note"} {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		fmt.Println(target, rec.Code)
	}
	// Output:
	// /import 200
	// /note 413
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
			req.Header.Set(router.HeaderHXRequestType, "partial")
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		fmt.Println(rec.Body.String())
	}
	// Output:
	// <li id="user-7">ann</li>
	// <h1>users</h1><p>/users</p>
}

func ExampleBase_WantsPartial() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })

	r.POST("/users", func(c *Context) error {
		u := &User{ID: "7", Name: "ann"}
		if c.WantsPartial() {
			return c.Render(http.StatusCreated, card(u))
		}
		return c.Redirect(http.StatusSeeOther, "/users")
	})

	for _, htmx := range []bool{true, false} {
		req := httptest.NewRequest(http.MethodPost, "/users", nil)
		if htmx {
			req.Header.Set(router.HeaderHXRequest, "true")
			req.Header.Set(router.HeaderHXRequestType, "partial")
		}
		rec := serveRequest(r, req)
		fmt.Println(rec.Code, rec.Header().Get("Location")+rec.Body.String())
	}
	// Output:
	// 201 <li id="user-7">ann</li>
	// 303 /users
}

func ExampleBase_RenderPartial() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })

	r.GET("/users", func(c *Context) error {
		return c.RenderPartial(http.StatusOK, card(&User{ID: "7", Name: "ann"}), page("users"))
	})

	var rec *httptest.ResponseRecorder
	for _, requestType := range []string{"", "partial", "full"} {
		req := httptest.NewRequest(http.MethodGet, "/users", nil)
		if requestType != "" {
			// htmx 4 sends "full" for a boosted link and a history restore.
			req.Header.Set(router.HeaderHXRequest, "true")
			req.Header.Set(router.HeaderHXRequestType, requestType)
		}
		rec = serveRequest(r, req)
		fmt.Println(rec.Body)
	}
	fmt.Println(strings.Join(rec.Header().Values("Vary"), ", "))
	// Output:
	// <h1>users</h1><p>/users</p>
	// <li id="user-7">ann</li>
	// <h1>users</h1><p>/users</p>
	// Hx-Request, Hx-Request-Type
}

func ExampleHTMXRequest_TargetID() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })

	r.GET("/users", func(c *Context) error {
		c.Vary(router.HeaderHXTarget)
		switch c.HTMX().TargetID() {
		case "user-list":
			return c.Render(http.StatusOK, card(&User{ID: "7", Name: "ann"}))
		default:
			return c.Render(http.StatusOK, page("users"))
		}
	})

	for _, target := range []string{"ul#user-list", "body"} {
		req := httptest.NewRequest(http.MethodGet, "/users", nil)
		req.Header.Set(router.HeaderHXRequest, "true")
		req.Header.Set(router.HeaderHXTarget, target)
		fmt.Println(serveRequest(r, req).Body)
	}
	// Output:
	// <li id="user-7">ann</li>
	// <h1>users</h1><p>/users</p>
}

func ExampleHTMXRequest_SourceID() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })

	// htmx 4 names the element that made the request in HX-Source. This is
	// what HX-Trigger carried as a request header in htmx 2.
	r.POST("/users/actions", func(c *Context) error {
		return c.String(http.StatusOK, c.HTMX().SourceID())
	})

	req := httptest.NewRequest(http.MethodPost, "/users/actions", nil)
	req.Header.Set(router.HeaderHXRequest, "true")
	req.Header.Set(router.HeaderHXSource, "button#delete-7")
	fmt.Println(serveRequest(r, req).Body)
	// Output:
	// delete-7
}

func ExampleBase_NewCookie() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.POST("/theme", func(c *Context) error {
		c.SetCookie(c.NewCookie("theme", "dark", 30*24*time.Hour))
		return c.NoContent(http.StatusNoContent)
	})
	r.GET("/theme", func(c *Context) error {
		return c.String(http.StatusOK, "theme: "+c.Cookie("theme"))
	})

	fmt.Println(serveRequest(r, httptest.NewRequest(http.MethodPost, "/theme", nil)).Header().Get("Set-Cookie"))

	// Behind a proxy that ends TLS the cookie is Secure. middleware.RealIP
	// decides whose X-Forwarded-Proto to believe.
	req := httptest.NewRequest(http.MethodPost, "/theme", nil)
	req.Header.Set(router.HeaderXForwardedProto, "https")
	fmt.Println(serveRequest(r, req).Header().Get("Set-Cookie"))

	req = httptest.NewRequest(http.MethodGet, "/theme", nil)
	req.Header.Set("Cookie", "theme=dark")
	fmt.Println(serveRequest(r, req).Body)
	// Output:
	// theme=dark; Path=/; Max-Age=2592000; HttpOnly; SameSite=Lax
	// theme=dark; Path=/; Max-Age=2592000; HttpOnly; Secure; SameSite=Lax
	// theme: dark
}

func ExampleBase_ClearCookie() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.POST("/logout", func(c *Context) error {
		c.ClearCookie("session")
		return c.Redirect(http.StatusSeeOther, "/")
	})

	rec := serveRequest(r, httptest.NewRequest(http.MethodPost, "/logout", nil))
	fmt.Println(rec.Code, rec.Header().Get("Set-Cookie"))
	// Output:
	// 303 session=; Path=/; Max-Age=0; HttpOnly; SameSite=Lax
}

func ExampleRouter_CookieCodec() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	// The key signs every cookie. NewCookieCodec panics under 32 bytes, so read
	// it from the environment rather than writing one here.
	r.CookieCodec(router.NewCookieCodec([]byte("32-bytes-of-key-material-for-hmac")))

	r.POST("/signin", func(c *Context) error {
		if err := c.SetSignedCookie(c.NewCookie("session", "ann", 12*time.Hour)); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
	r.GET("/me", func(c *Context) error {
		name, err := c.SignedCookie("session")
		if err != nil {
			return router.ErrUnauthorized
		}
		return c.String(http.StatusOK, name)
	})

	signin := serveRequest(r, httptest.NewRequest(http.MethodPost, "/signin", nil))
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Cookie", signin.Header().Get("Set-Cookie"))

	fmt.Println(signin.Code)
	fmt.Println(serveRequest(r, req).Body)
	// Output:
	// 204
	// ann
}

func ExampleNewCookieCodec_rotation() {
	oldKey := []byte("32-bytes-of-key-material-for-hmac")
	newKey := []byte("32-more-bytes-of-fresh-key-material")
	signedBefore := router.NewCookieCodec(oldKey).Encode("session", []byte("ann"))

	// Sign with newKey, and keep reading what oldKey signed until it runs out.
	codec := router.NewCookieCodec(newKey, oldKey)
	value, err := codec.Decode("session", signedBefore)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(string(value))

	// A value signed now carries newKey, so a codec that has only oldKey
	// refuses it.
	_, err = router.NewCookieCodec(oldKey).Decode("session", codec.Encode("session", []byte("ann")))
	fmt.Println(err)
	// Output:
	// ann
	// router: the signed cookie does not verify
}

// The codec of the router also signs the flash cookie, so a message survives
// the redirect after a form.
func ExampleRouter_CookieCodec_flash() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	// The key signs the cookie. NewCookieCodec panics under 32 bytes, so read
	// it from the environment rather than writing one here.
	r.CookieCodec(router.NewCookieCodec([]byte("32-bytes-of-key-material-for-hmac")))

	r.POST("/users", func(c *Context) error {
		if err := c.AddFlash(router.Flash{Kind: "success", Message: "user created"}); err != nil {
			return err
		}
		return c.Redirect(http.StatusSeeOther, "/users")
	})
	r.GET("/users", func(c *Context) error {
		// Flashes reads once: it clears the cookie on the way out.
		return c.Stringf(http.StatusOK, "%v", c.Flashes())
	})

	created := serveRequest(r, httptest.NewRequest(http.MethodPost, "/users", nil))
	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	for _, c := range created.Result().Cookies() {
		req.AddCookie(c) // AddCookie sends only name=value, as a browser does.
	}

	fmt.Println(created.Code, created.Header().Get("Location"))
	fmt.Println(serveRequest(r, req).Body)
	// Output:
	// 303 /users
	// [{success user created}]
}

// toasts shows the flash messages. A layout runs it on every page, and a
// partial runs it out of band.
func toasts(ctx context.Context, w io.Writer) error {
	c, ok := router.FromContext(ctx)
	if !ok {
		return nil
	}
	if _, err := io.WriteString(w, `<div id="toasts" hx-swap-oob="true">`); err != nil {
		return err
	}
	for _, f := range c.Flashes() {
		if _, err := fmt.Fprintf(w, "<p class=%q>%s</p>", f.Kind, f.Message); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "</div>")
	return err
}

// An htmx partial shows the flash in place, and a plain form post carries it
// across the redirect. A flash read in the request that added it never
// reaches the browser as a cookie.
func ExampleBase_Flashes() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.CookieCodec(router.NewCookieCodec([]byte("32-bytes-of-key-material-for-hmac")))

	r.POST("/users", func(c *Context) error {
		u := &User{ID: "7", Name: "ann"}
		if err := c.AddFlash(router.Flash{Kind: "success", Message: "user created"}); err != nil {
			return err
		}
		if !c.WantsPartial() {
			return c.Redirect(http.StatusSeeOther, "/users")
		}
		// Render buffers, so toasts may call Flashes before the headers go out.
		return c.Render(http.StatusCreated, router.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			if err := card(u).Render(ctx, w); err != nil {
				return err
			}
			return toasts(ctx, w)
		}))
	})

	for _, htmx := range []bool{true, false} {
		req := httptest.NewRequest(http.MethodPost, "/users", nil)
		if htmx {
			req.Header.Set(router.HeaderHXRequest, "true")
			req.Header.Set(router.HeaderHXRequestType, "partial")
		}
		rec := serveRequest(r, req)
		fmt.Println(rec.Code, len(rec.Header().Values("Set-Cookie")), rec.Header().Get("Location")+rec.Body.String())
	}
	// Output:
	// 201 0 <li id="user-7">ann</li><div id="toasts" hx-swap-oob="true"><p class="success">user created</p></div>
	// 303 1 /users
}

// CookieCodecOf hands test tooling the codec of a router, so it can read the
// signed cookies the router set.
func ExampleCookieCodecOf() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	codec := router.NewCookieCodec([]byte("32-bytes-of-key-material-for-hmac"))
	r.CookieCodec(codec)

	fmt.Println(router.CookieCodecOf(r) == codec)
	fmt.Println(router.CookieCodecOf(http.NotFoundHandler()) != nil)
	// Output:
	// true
	// false
}

// SetCookieCodecForTest lets a test call a handler that signs cookies without
// building a router.
func ExampleSetCookieCodecForTest() {
	codec := router.NewCookieCodec([]byte("32-bytes-of-key-material-for-hmac"))
	rec := httptest.NewRecorder()
	b := router.NewBase(rec, httptest.NewRequest(http.MethodPost, "/signin", nil))
	router.SetCookieCodecForTest(b, codec)

	if err := b.SetSignedCookie(b.NewCookie("session", "ann", time.Hour)); err != nil {
		fmt.Println(err)
		return
	}
	c, err := http.ParseSetCookie(rec.Header().Get("Set-Cookie"))
	if err != nil {
		fmt.Println(err)
		return
	}
	value, err := codec.Decode("session", c.Value)
	fmt.Println(string(value), err)
	// Output:
	// ann <nil>
}

func serveRequest(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func ExampleBase_RenderStream() {
	half := router.ComponentFunc(func(_ context.Context, w io.Writer) error {
		//nolint:errcheck // The next line reports the failure that matters.
		fmt.Fprint(w, "<h1>half a page</h1>")
		return errors.New("the rest of the page failed")
	})

	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.Logger(slog.New(slog.DiscardHandler))
	// Render buffers, so a failure halfway through still becomes a 500.
	r.GET("/buffered", func(c *Context) error { return c.Render(http.StatusOK, half) })
	// RenderStream writes as it goes, so the 200 has already left.
	r.GET("/streamed", func(c *Context) error { return c.RenderStream(http.StatusOK, half) })

	fmt.Println(serve(r, http.MethodGet, "/buffered"))
	fmt.Println(serve(r, http.MethodGet, "/streamed"))
	// Output:
	// 500 Internal Server Error
	// 200 <h1>half a page</h1>
}

func ExampleWrapHandler() {
	legacy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The route parameters reach a stdlib handler through PathValue.
		//nolint:errcheck // A stdlib handler has nowhere to return it.
		fmt.Fprintf(w, "user %s", r.PathValue("id"))
	})

	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.GET("/users/{id}", router.WrapHandler[*Context](legacy))

	fmt.Println(serve(r, http.MethodGet, "/users/7"))
	// Output:
	// 200 user 7
}

func ExampleHTTPError_WithMessage() {
	// Every sentinel is a template. WithMessage copies it, so the original
	// keeps its own message and errors.Is still matches on the status.
	missing := router.ErrNotFound.WithMessage("no user %s", "7")
	invalid := router.ErrUnprocessableEntity.WithDetails([]router.FieldError{
		{Field: "name", Message: "is required"},
	})

	fmt.Println(missing, errors.Is(missing, router.ErrNotFound))
	fmt.Println(router.ErrNotFound)
	fmt.Println(router.StatusOf(invalid), invalid.Details)
	// Output:
	// 404 no user 7 true
	// 404 Not Found
	// 422 [name: is required]
}

func ExampleHandleError() {
	errLocked := errors.New("the row is locked")

	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.Logger(slog.New(slog.DiscardHandler))
	r.ErrorHandler(func(c *Context, err error) error {
		if errors.Is(err, errLocked) {
			return c.String(http.StatusLocked, "locked")
		}
		return router.DefaultErrorHandler(c, err)
	})
	// A metrics middleware answers the error itself, so it reads the status
	// the error handler wrote rather than guessing it from err.
	r.Use(func(next router.HandlerFunc[*Context]) router.HandlerFunc[*Context] {
		return func(c *Context) error {
			err := next(c)
			router.HandleError(c, err)
			fmt.Println("measured", c.Response().Status)
			return err
		}
	})
	r.GET("/rows/{id}", func(*Context) error { return errLocked })

	fmt.Println(serve(r, http.MethodGet, "/rows/7"))
	// Output:
	// measured 423
	// 423 locked
}

func ExampleJSONErrorHandler() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.Logger(slog.New(slog.DiscardHandler))
	r.Host("api.example.com", func(h *router.Router[*Context]) {
		h.ErrorHandler(router.JSONErrorHandler[*Context])
		h.GET("/v1/users/{id}", func(c *Context) error {
			return router.ErrNotFound.WithMessage("no user %s", c.Param("id"))
		})
	})
	r.Host("example.com", func(h *router.Router[*Context]) {
		h.GET("/", func(c *Context) error { return c.String(http.StatusOK, "landing") })
	})

	fmt.Println(serveHost(r, http.MethodGet, "api.example.com", "/v1/users/9"))
	fmt.Println(serveHost(r, http.MethodGet, "api.example.com", "/v2/users"))
	fmt.Println(serveHost(r, http.MethodGet, "example.com", "/missing"))
	// Output:
	// 404 {"error":{"status":404,"message":"no user 9"}}
	// 404 {"error":{"status":404,"message":"Not Found"}}
	// 404 Not Found
}

func ExampleHTTPErrorOf() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.Logger(slog.New(slog.DiscardHandler))
	// An error page of your own. The same handler, rendering a fragment,
	// suits an htmx scope, since htmx 4 swaps a 4xx or a 5xx into its target.
	r.ErrorHandler(func(c *Context, err error) error {
		he := router.HTTPErrorOf(err)
		return c.HTML(he.Status, "<h1>"+html.EscapeString(he.Message)+"</h1>")
	})
	r.GET("/users/{id}", func(c *Context) error {
		return router.ErrNotFound.WithMessage("no user %s", c.Param("id"))
	})
	r.GET("/report", func(*Context) error { return errors.New("db: connection refused") })

	fmt.Println(serve(r, http.MethodGet, "/users/9"))
	fmt.Println(serve(r, http.MethodGet, "/report"))
	// Output:
	// 404 <h1>no user 9</h1>
	// 500 <h1>Internal Server Error</h1>
}
