package routertest_test

import (
	"fmt"
	"log/slog"
	"net/http"
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
		routertest.WithPattern("/users/{id}"),
		routertest.WithParams(map[string]string{"id": "7"}),
	)
	_ = showUser(c)
	if got := rec.Body.String(); got != "user 7" {
		tb.Errorf("body = %q, want %q", got, "user 7")
	}
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
