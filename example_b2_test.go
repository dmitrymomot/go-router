package router_test

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"github.com/dmitrymomot/go-router"
)

func ExampleProblemErrorHandler() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.Logger(slog.New(slog.DiscardHandler))
	r.ErrorHandler(router.ProblemErrorHandler[*Context](false))
	r.POST("/users", func(*Context) error {
		return router.ErrUnprocessableEntity.
			WithMessage("the form has errors").
			WithDetails([]router.FieldError{{Field: "email", Message: "is required"}})
	})

	fmt.Println(serve(r, http.MethodPost, "/users"))
	// Output:
	// 422 {"type":"about:blank","title":"Unprocessable Entity","status":422,"detail":"the form has errors","instance":"/users","errors":[{"field":"email","message":"is required"}]}
}

func ExampleBase_LocalRedirect() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.Logger(slog.New(slog.DiscardHandler))
	r.GET("/login", func(c *Context) error {
		// next comes from the client, so only a path on this host is followed.
		return c.LocalRedirect(http.StatusSeeOther, c.Query("next"))
	})

	fmt.Println(serveLocation(r, "", "/login?next=/account"))
	fmt.Println(serve(r, http.MethodGet, "/login?next=//evil.example"))
	// Output:
	// 303 /account
	// 400 redirect target is not a local path
}

func ExampleBase_CheckETag() {
	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.Logger(slog.New(slog.DiscardHandler))
	r.GET("/report", func(c *Context) error {
		data := []byte(`{"total":42}`)
		if c.CheckETag(router.ETagOf(data)) {
			return nil
		}
		return c.Blob(http.StatusOK, router.MIMEApplicationJSON, data)
	})

	first := httptest.NewRecorder()
	r.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/report", nil))
	fmt.Println(first.Code, first.Body.String())

	req := httptest.NewRequest(http.MethodGet, "/report", nil)
	req.Header.Set(router.HeaderIfNoneMatch, first.Header().Get(router.HeaderETag))
	again := httptest.NewRecorder()
	r.ServeHTTP(again, req)
	fmt.Println(again.Code, again.Body.Len())
	// Output:
	// 200 {"total":42}
	// 304 0
}
