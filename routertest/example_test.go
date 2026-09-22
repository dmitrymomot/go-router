package routertest_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
)

// showUser is the handler the examples below drive.
func showUser(c *appContext) error {
	return c.Stringf(http.StatusOK, "user %s", c.Param("id"))
}

func Example() {
	r := router.New(newContext)
	r.GET("/users/{id}", showUser)
	r.POST("/users", func(c *appContext) error {
		in, err := c.Bind[user]()
		if err != nil {
			return err
		}
		return c.Stringf(http.StatusCreated, "created %s", in.Name)
	})

	got := routertest.Get(r, "/users/7")
	made := routertest.Do(r, http.MethodPost, "/users",
		routertest.JSONBody(user{Name: "ann", Age: 30}))

	fmt.Println(got.StatusCode, got.String())
	fmt.Println(made.StatusCode, made.String())
	// Output:
	// 200 user 7
	// 201 created ann
}

// NewContext builds the context a route would have built, so that a handler
// can be tested on its own. The factory must give back a usable router.Base:
// embed it by value, or fill an embedded pointer with router.NewBase.
func ExampleNewContext() {
	// tb is the *testing.T of the test that runs this.
	var tb testing.TB

	c, rec := routertest.NewContext(tb, newContext,
		routertest.WithTarget(http.MethodGet, "/users/7", routertest.Header("Accept", router.MIMETextPlain)),
		routertest.WithPattern("/users/{id}"),
		routertest.WithParams(map[string]string{"id": "7"}),
	)
	if err := showUser(c); err != nil {
		tb.Fatal(err)
	}
	routertest.Recorded(rec).Expect(tb).Status(http.StatusOK).Body("user 7")
}

// Recorded reads back what a handler wrote to a recorder of its own.
func ExampleRecorded() {
	r := router.New(newContext)
	r.POST("/users", func(c *appContext) error {
		c.SetHeader(router.HeaderLocation, "/users/7")
		return c.String(http.StatusCreated, "created")
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/users", nil))
	res := routertest.Recorded(rec)
	fmt.Println(res.StatusCode, res.Header.Get(router.HeaderLocation), res.String())
	// Output:
	// 201 /users/7 created
}

type tenantKey struct{}

// Context gives the request a context of its own, such as one that carries
// what a middleware would have put there.
func ExampleContext() {
	r := router.New(newContext)
	r.GET("/whoami", func(c *appContext) error {
		return c.Stringf(http.StatusOK, "tenant %v", c.Value(tenantKey{}))
	})

	ctx := context.WithValue(context.Background(), tenantKey{}, "acme")
	fmt.Println(routertest.Get(r, "/whoami", routertest.Context(ctx)))
	// Output:
	// tenant acme
}

// RemoteAddr sets the address the request comes from, for a handler or a
// middleware that keys on the caller.
func ExampleRemoteAddr() {
	r := router.New(newContext)
	r.GET("/ip", func(c *appContext) error {
		return c.String(http.StatusOK, c.Request().RemoteAddr)
	})

	fmt.Println(routertest.Get(r, "/ip", routertest.RemoteAddr("203.0.113.7:54321")))
	// Output:
	// 203.0.113.7:54321
}

// Expect chains the checks of one answer. Status and Redirect stop the test on
// a miss; the other checks report it and go on.
func ExampleResponse_Expect() {
	// tb is the *testing.T of the test that runs this.
	var tb testing.TB

	r := router.New(newContext)
	r.GET("/users/{id}", showUser)

	routertest.Get(r, "/users/7").Expect(tb).
		Status(http.StatusOK).
		ContentType(router.MIMETextPlain).
		Contains("user 7").
		NotContains("error")
}

// A Client keeps the cookies of every answer and sends them back, as a
// browser does, so a test can sign in once and go on as that user.
func ExampleClient() {
	// tb is the *testing.T of the test that runs this.
	var tb testing.TB

	r := router.New(newContext)
	r.POST("/login", func(c *appContext) error {
		c.SetCookie(c.NewCookie("session", c.FormValue("name"), time.Hour))
		return c.Redirect(http.StatusSeeOther, "/me")
	})
	r.GET("/me", func(c *appContext) error {
		return c.Stringf(http.StatusOK, "hello %s", c.Cookie("session"))
	})

	cl := routertest.NewClient(tb, r, routertest.Host("app.example.com"))
	cl.Do(http.MethodPost, "/login", routertest.FormBody(url.Values{"name": {"ann"}})).
		Expect(tb).Redirect(http.StatusSeeOther, "/me")
	cl.Get("/me").Expect(tb).Body("hello ann")

	if cl.Cookie("session").Value != "ann" {
		tb.Error("the client lost the session")
	}
	cl.SetCookie(&http.Cookie{Name: "session", Value: "bob"})
	cl.Get("/me").Expect(tb).Body("hello bob")
}

// Follow sends the GET that a browser sends after a redirect, with the
// cookies the redirect set.
func ExampleClient_Follow() {
	// tb is the *testing.T of the test that runs this.
	var tb testing.TB

	r := router.New(newContext)
	r.POST("/users", func(c *appContext) error {
		return c.Redirect(http.StatusSeeOther, "/users/7")
	})
	r.GET("/users/{id}", showUser)

	cl := routertest.NewClient(tb, r)
	res := cl.Do(http.MethodPost, "/users")
	cl.Follow(res).Expect(tb).Status(http.StatusOK).Body("user 7")
}

// Requests sends one request to each route, here to prove that every route
// but the health check asks for a key.
func ExampleRequests() {
	r := router.New(newContext)
	r.Host("api.example.com", func(api *router.Router[*appContext]) {
		api.GET("/v1/health", func(c *appContext) error { return c.NoContent(http.StatusNoContent) })
		api.Route("/v1", func(v1 *router.Router[*appContext]) {
			v1.Use(func(next router.HandlerFunc[*appContext]) router.HandlerFunc[*appContext] {
				return func(c *appContext) error {
					if c.Request().Header.Get("X-Api-Key") != "secret" {
						return router.ErrUnauthorized
					}
					return next(c)
				}
			})
			v1.GET("/users/{id:int}", showUser)
			v1.DELETE("/users/{id:int}", showUser)
		})
	})

	routes := slices.DeleteFunc(r.Routes(), func(rt router.Route) bool { return rt.Pattern == "/v1/health" })
	for rt, req := range routertest.Requests(routes, nil) {
		fmt.Println(rt.Method, rt.Pattern, "->", routertest.Serve(r, req).StatusCode)
	}
	// Output:
	// DELETE /v1/users/{id:int} -> 401
	// GET /v1/users/{id:int} -> 401
}

// SignedCookie reads a signed cookie back through the codec of the router
// that set it.
func ExampleSignedCookie() {
	r := router.New(newContext)
	r.CookieCodec(router.NewCookieCodec([]byte("32-bytes-of-key-material-for-hmac")))
	r.POST("/signin", func(c *appContext) error {
		if err := c.SetSignedCookie(c.NewCookie("session", "ann", time.Hour)); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})

	fmt.Println(routertest.SignedCookie(routertest.Do(r, http.MethodPost, "/signin"), "session"))
	// Output:
	// ann true
}

// Flashes reads the messages a handler left for the page after its redirect.
func ExampleFlashes() {
	r := router.New(newContext)
	r.CookieCodec(router.NewCookieCodec([]byte("32-bytes-of-key-material-for-hmac")))
	r.POST("/users", func(c *appContext) error {
		if err := c.AddFlash(router.Flash{Kind: "success", Message: "user created"}); err != nil {
			return err
		}
		return c.Redirect(http.StatusSeeOther, "/users")
	})

	res := routertest.Do(r, http.MethodPost, "/users")
	fmt.Println(res.StatusCode, routertest.Flashes(res))
	// Output:
	// 303 [{success user created}]
}

// FlashCookie sends the messages a redirect would have left, for a test of the
// page that shows them.
func ExampleFlashCookie() {
	codec := router.NewCookieCodec([]byte("32-bytes-of-key-material-for-hmac"))
	r := router.New(newContext)
	r.CookieCodec(codec)
	r.GET("/users", func(c *appContext) error {
		return c.Stringf(http.StatusOK, "%v", c.Flashes())
	})

	res := routertest.Get(r, "/users", routertest.FlashCookie(codec, router.Flash{Kind: "success", Message: "user created"}))
	fmt.Println(res)
	// Output:
	// [{success user created}]
}

// WithCookieCodec gives a context built without a router the codec that
// router.Router.CookieCodec would.
func ExampleWithCookieCodec() {
	// tb is the *testing.T of the test that runs this.
	var tb testing.TB
	codec := router.NewCookieCodec([]byte("32-bytes-of-key-material-for-hmac"))

	c, _ := routertest.NewContext(tb, newContext, routertest.WithCookieCodec(codec))
	if err := c.SetSignedCookie(c.NewCookie("session", "ann", time.Hour)); err != nil {
		tb.Fatal(err)
	}
}

func ExampleEvents() {
	r := router.New(newContext)
	r.GET("/stream", func(c *appContext) error {
		names := make(chan string, 2)
		names <- "ann"
		names <- "bob"
		close(names)
		return router.ServeSSE(c, names, router.SSEText[string]("user"))
	})

	for _, e := range routertest.Events(routertest.Get(r, "/stream")) {
		fmt.Println(e.Name, e.Data)
	}
	// Output:
	// user ann
	// user bob
}

// ErrorBody reads an answer of router.JSONErrorHandler back, with the fields
// that failed validation.
func ExampleResponse_ErrorBody() {
	r := router.New(newContext)
	r.Logger(slog.New(slog.DiscardHandler))
	r.ErrorHandler(router.JSONErrorHandler[*appContext])
	r.POST("/signup", func(c *appContext) error {
		_, err := c.Bind[signup]()
		return err
	})

	res := routertest.Do(r, http.MethodPost, "/signup", routertest.JSONBody(signup{Name: "ann"}))
	body, err := res.ErrorBody()
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(body.Status, body.Message)
	for _, f := range body.Details.([]router.FieldError) {
		fmt.Println(f.Field, f.Message)
	}
	// Output:
	// 422 Unprocessable Entity
	// email is required
}

// FieldErrors checks which fields an answer of router.JSONErrorHandler names.
func ExampleExpect_FieldErrors() {
	// tb is the *testing.T of the test that runs this.
	var tb testing.TB

	r := router.New(newContext)
	r.ErrorHandler(router.JSONErrorHandler[*appContext])
	r.POST("/signup", func(c *appContext) error {
		_, err := c.Bind[signup]()
		return err
	})

	routertest.Do(r, http.MethodPost, "/signup", routertest.JSONBody(signup{Name: "ann"})).Expect(tb).
		Status(http.StatusUnprocessableEntity).
		FieldErrors("email")
}
