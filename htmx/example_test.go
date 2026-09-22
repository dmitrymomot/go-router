package htmx_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/htmx"
)

type Context struct {
	router.Base
}

type User struct {
	ID   string
	Name string
}

func newRouter() *router.Router[*Context] {
	return router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
}

func serveRequest(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func card(u *User) router.ComponentFunc {
	return func(_ context.Context, w io.Writer) error {
		_, err := fmt.Fprintf(w, "<li id=%q>%s</li>", "user-"+u.ID, u.Name)
		return err
	}
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

func ExampleNewResponse() {
	r := newRouter()

	r.PUT("/users/{id}", func(c *Context) error {
		u := &User{ID: c.Param("id"), Name: "ann"}
		hx := htmx.NewResponse(c).
			Retarget("#user-" + u.ID).
			Reswap(htmx.SwapOuterHTML).
			Trigger("user-saved")
		if err := hx.Err(); err != nil {
			return err
		}
		return c.Render(http.StatusOK, card(u))
	})

	rec := serveRequest(r, httptest.NewRequest(http.MethodPut, "/users/7", nil))
	fmt.Println(rec.Code)
	fmt.Println(rec.Header().Get(htmx.HeaderRetarget), rec.Header().Get(htmx.HeaderReswap))
	fmt.Println(rec.Header().Get(htmx.HeaderTrigger))
	fmt.Println(rec.Body.String())
	// Output:
	// 200
	// #user-7 outerHTML
	// user-saved
	// <li id="user-7">ann</li>
}

func ExampleResponse_Redirect() {
	r := newRouter()
	r.POST("/join", func(c *Context) error { return htmx.NewResponse(c).Redirect("/chat") })

	for _, hx := range []bool{true, false} {
		req := httptest.NewRequest(http.MethodPost, "/join", nil)
		if hx {
			req.Header.Set(htmx.HeaderRequest, "true")
		}
		rec := serveRequest(r, req)
		fmt.Printf("%d HX-Redirect=%q Location=%q\n", rec.Code,
			rec.Header().Get(htmx.HeaderRedirect),
			rec.Header().Get("Location"))
	}
	// Output:
	// 200 HX-Redirect="/chat" Location=""
	// 303 HX-Redirect="" Location="/chat"
}

func ExamplePartial() {
	r := newRouter()

	r.GET("/users", htmx.Partial(
		func(c *Context) error { return c.Render(http.StatusOK, card(&User{ID: "7", Name: "ann"})) },
		func(c *Context) error { return c.Render(http.StatusOK, page("users")) },
	))

	for _, hx := range []bool{true, false} {
		req := httptest.NewRequest(http.MethodGet, "/users", nil)
		if hx {
			req.Header.Set(htmx.HeaderRequest, "true")
			req.Header.Set(htmx.HeaderRequestType, "partial")
		}
		fmt.Println(serveRequest(r, req).Body.String())
	}
	// Output:
	// <li id="user-7">ann</li>
	// <h1>users</h1><p>/users</p>
}

func ExampleWantsPartial() {
	r := newRouter()

	r.POST("/users", func(c *Context) error {
		u := &User{ID: "7", Name: "ann"}
		if htmx.WantsPartial(c) {
			return c.Render(http.StatusCreated, card(u))
		}
		return c.Redirect(http.StatusSeeOther, "/users")
	})

	for _, hx := range []bool{true, false} {
		req := httptest.NewRequest(http.MethodPost, "/users", nil)
		if hx {
			req.Header.Set(htmx.HeaderRequest, "true")
			req.Header.Set(htmx.HeaderRequestType, "partial")
		}
		rec := serveRequest(r, req)
		fmt.Println(rec.Code, rec.Header().Get("Location")+rec.Body.String())
	}
	// Output:
	// 201 <li id="user-7">ann</li>
	// 303 /users
}

func ExampleRenderPartial() {
	r := newRouter()

	r.GET("/users", func(c *Context) error {
		return htmx.RenderPartial(c, http.StatusOK, card(&User{ID: "7", Name: "ann"}), page("users"))
	})

	var rec *httptest.ResponseRecorder
	for _, requestType := range []string{"", "partial", "full"} {
		req := httptest.NewRequest(http.MethodGet, "/users", nil)
		if requestType != "" {
			// htmx 4 sends "full" for a boosted link and a history restore.
			req.Header.Set(htmx.HeaderRequest, "true")
			req.Header.Set(htmx.HeaderRequestType, requestType)
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

func ExampleRequest_TargetID() {
	r := newRouter()

	r.GET("/users", func(c *Context) error {
		c.Vary(htmx.HeaderTarget)
		switch htmx.RequestOf(c.Request()).TargetID() {
		case "user-list":
			return c.Render(http.StatusOK, card(&User{ID: "7", Name: "ann"}))
		default:
			return c.Render(http.StatusOK, page("users"))
		}
	})

	for _, target := range []string{"ul#user-list", "body"} {
		req := httptest.NewRequest(http.MethodGet, "/users", nil)
		req.Header.Set(htmx.HeaderRequest, "true")
		req.Header.Set(htmx.HeaderTarget, target)
		fmt.Println(serveRequest(r, req).Body)
	}
	// Output:
	// <li id="user-7">ann</li>
	// <h1>users</h1><p>/users</p>
}

func ExampleRequest_SourceID() {
	r := newRouter()

	// htmx 4 names the element that made the request in HX-Source. This is
	// what HX-Trigger carried as a request header in htmx 2.
	r.POST("/users/actions", func(c *Context) error {
		return c.String(http.StatusOK, htmx.RequestOf(c.Request()).SourceID())
	})

	req := httptest.NewRequest(http.MethodPost, "/users/actions", nil)
	req.Header.Set(htmx.HeaderRequest, "true")
	req.Header.Set(htmx.HeaderSource, "button#delete-7")
	fmt.Println(serveRequest(r, req).Body)
	// Output:
	// delete-7
}
