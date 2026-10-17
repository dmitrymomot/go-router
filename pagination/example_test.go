package pagination_test

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/pagination"
	"github.com/dmitrymomot/go-router/routertest"
)

func ExamplePage() {
	type listUsers struct {
		pagination.Page
		Role string `query:"role"`
	}

	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.Logger(slog.New(slog.DiscardHandler))
	r.ErrorHandler(router.JSONErrorHandler[*appContext](false))
	r.GET("/users", func(c *appContext) error {
		req, err := c.Bind[listUsers]()
		if err != nil {
			return err
		}
		// This endpoint caps its pages at 50 rows.
		return c.String(http.StatusOK, fmt.Sprintf("LIMIT %d OFFSET %d", req.Limit(50), req.Offset(50)))
	})

	fmt.Println(routertest.Get(r, "/users").String())
	fmt.Println(routertest.Get(r, "/users?page=3&per_page=80").String())
	res := routertest.Get(r, "/users?page=0")
	fmt.Println(res.StatusCode, res.String())
	// Output:
	// LIMIT 20 OFFSET 0
	// LIMIT 50 OFFSET 100
	// 422 {"error":{"status":422,"message":"Unprocessable Entity","details":[{"field":"page","message":"must be 1 or more"}]}}
}

func ExampleCursor() {
	type feed struct {
		pagination.Cursor
	}

	r := router.New(func(http.ResponseWriter, *http.Request) *appContext { return new(appContext) })
	r.GET("/feed", func(c *appContext) error {
		req, err := c.Bind[feed]()
		if err != nil {
			return err
		}
		// One row more than the page tells whether a next page exists.
		return c.String(http.StatusOK, fmt.Sprintf("WHERE id > %q LIMIT %d", req.After, req.Limit(100)+1))
	})

	fmt.Println(routertest.Get(r, "/feed?after=42&limit=10").String())
	// Output:
	// WHERE id > "42" LIMIT 11
}
