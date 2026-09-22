package sse_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/sse"
)

type Context struct{ router.Base }

type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func newContext(http.ResponseWriter, *http.Request) *Context { return new(Context) }

func ExampleServe() {
	r := router.New(newContext)

	r.GET("/users/stream", func(c *Context) error {
		users := make(chan *User, 2)
		users <- &User{ID: "7", Name: "ann"}
		users <- &User{ID: "8", Name: "bob"}
		close(users)

		return sse.Serve(c, users, sse.JSON[*User]("user"), sse.Heartbeat(15*time.Second))
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

func card(u *User) router.ComponentFunc {
	return func(_ context.Context, w io.Writer) error {
		_, err := fmt.Fprintf(w, "<li id=%q>%s</li>", "user-"+u.ID, u.Name)
		return err
	}
}

func ExampleNewStream() {
	stream := sse.NewStream(sse.Component("user", card), sse.Retry(3*time.Second))

	r := router.New(newContext)
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

func ExampleOpen() {
	r := router.New(newContext)

	r.GET("/ticks", func(c *Context) error {
		s, err := sse.Open(c, http.StatusOK)
		if err != nil {
			return err
		}
		for i := range 2 {
			if err := s.Send(sse.Event{ID: fmt.Sprint(i + 1), Name: "tick", Data: "one\ntwo"}); err != nil {
				return err
			}
		}
		return nil
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ticks", nil))
	fmt.Print(rec.Body.String())
	// Output:
	// id: 1
	// event: tick
	// data: one
	// data: two
	//
	// id: 2
	// event: tick
	// data: one
	// data: two
}
