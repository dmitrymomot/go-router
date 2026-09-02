package routertest_test

import (
	"fmt"
	"net/http"
	"testing"

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
