package cookie_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/cookie"
	"github.com/dmitrymomot/go-router/htmx"
)

// Context is the context of the app. It keeps the codec, which the factory
// sets on every request.
type Context struct {
	router.Base
	Cookies *cookie.Codec
}

// newRouter builds a router whose contexts carry cc.
func newRouter(cc *cookie.Codec) *router.Router[*Context] {
	return router.New(func(http.ResponseWriter, *http.Request) *Context {
		return &Context{Cookies: cc}
	})
}

func serveRequest(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func ExampleCodec() {
	// The key signs every cookie. NewCodec panics under 32 bytes, so read it
	// from the environment rather than writing one here.
	r := newRouter(cookie.NewCodec([]byte("32-bytes-of-key-material-for-hmac")))

	r.POST("/signin", func(c *Context) error {
		if err := c.Cookies.Set(c, c.NewCookie("session", "ann", 12*time.Hour)); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
	r.GET("/me", func(c *Context) error {
		name, err := c.Cookies.Get(c, "session")
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

func ExampleNewCodec_rotation() {
	oldKey := []byte("32-bytes-of-key-material-for-hmac")
	newKey := []byte("32-more-bytes-of-fresh-key-material")
	expires := time.Now().Add(time.Hour)
	signedBefore := cookie.NewCodec(oldKey).Encode("session", []byte("ann"), expires)

	// Sign with newKey, and keep reading what oldKey signed until it runs out.
	codec := cookie.NewCodec(newKey, oldKey)
	value, err := codec.Decode("session", signedBefore)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(string(value))

	// A value signed now carries newKey, so a codec that has only oldKey
	// refuses it.
	_, err = cookie.NewCodec(oldKey).Decode("session", codec.Encode("session", []byte("ann"), expires))
	fmt.Println(err)
	// Output:
	// ann
	// cookie: the signed cookie does not verify
}

// The codec also signs the flash cookie, so a message survives the redirect
// after a form.
func ExampleCodec_AddFlash() {
	r := newRouter(cookie.NewCodec([]byte("32-bytes-of-key-material-for-hmac")))

	r.POST("/users", func(c *Context) error {
		if err := c.Cookies.AddFlash(c, cookie.Flash{Kind: "success", Message: "user created"}); err != nil {
			return err
		}
		return c.Redirect(http.StatusSeeOther, "/users")
	})
	r.GET("/users", func(c *Context) error {
		// Flashes reads once: it clears the cookie on the way out.
		return c.String(http.StatusOK, fmt.Sprintf("%v", c.Cookies.Flashes(c)))
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

// toasts shows the flash messages of c. A layout runs it on every page, and a
// partial runs it out of band.
func toasts(c *Context) router.ComponentFunc {
	return func(_ context.Context, w io.Writer) error {
		if _, err := io.WriteString(w, `<div id="toasts" hx-swap-oob="true">`); err != nil {
			return err
		}
		for _, f := range c.Cookies.Flashes(c) {
			if _, err := fmt.Fprintf(w, "<p class=%q>%s</p>", f.Kind, f.Message); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, "</div>")
		return err
	}
}

// An htmx partial shows the flash in place, and a plain form post carries it
// across the redirect. A flash read in the request that added it never
// reaches the browser as a cookie.
func ExampleCodec_Flashes() {
	r := newRouter(cookie.NewCodec([]byte("32-bytes-of-key-material-for-hmac")))

	r.POST("/users", func(c *Context) error {
		if err := c.Cookies.AddFlash(c, cookie.Flash{Kind: "success", Message: "user created"}); err != nil {
			return err
		}
		if !htmx.WantsPartial(c) {
			return c.Redirect(http.StatusSeeOther, "/users")
		}
		// Render buffers, so toasts may call Flashes before the headers go out.
		return c.Render(http.StatusCreated, router.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			if _, err := io.WriteString(w, `<li id="user-7">ann</li>`); err != nil {
				return err
			}
			return toasts(c).Render(ctx, w)
		}))
	})

	for _, hx := range []bool{true, false} {
		req := httptest.NewRequest(http.MethodPost, "/users", nil)
		if hx {
			req.Header.Set(htmx.HeaderRequest, "true")
			req.Header.Set(htmx.HeaderRequestType, "partial")
		}
		rec := serveRequest(r, req)
		fmt.Println(rec.Code, len(rec.Header().Values("Set-Cookie")), rec.Header().Get("Location")+rec.Body.String())
	}
	// Output:
	// 201 0 <li id="user-7">ann</li><div id="toasts" hx-swap-oob="true"><p class="success">user created</p></div>
	// 303 1 /users
}
