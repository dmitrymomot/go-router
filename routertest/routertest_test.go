package routertest_test

import (
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
)

type appContext struct {
	router.Base
}

type user struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func newRouter() *router.Router[*appContext] {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext {
		return new(appContext)
	})
	r.POST("/users", func(c *appContext) error {
		in, err := c.Bind[user]()
		if err != nil {
			return err
		}
		in.Age++
		return c.JSON(http.StatusCreated, in)
	})
	r.GET("/users/{id}", func(c *appContext) error {
		return c.String(http.StatusOK, "user "+c.Param("id"))
	})
	r.POST("/login", func(c *appContext) error {
		in, err := c.BindForm[user]()
		if err != nil {
			return err
		}
		c.SetHeader("X-Who", in.Name)
		return c.NoContent(http.StatusNoContent)
	})
	return r
}

func TestJSONRoundTrip(t *testing.T) {
	res := routertest.Do(newRouter(), http.MethodPost, "/users",
		routertest.JSONBody(user{Name: "ann", Age: 30}))
	res.AssertStatus(t, http.StatusCreated)

	got, err := res.JSON[user]()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != (user{Name: "ann", Age: 31}) {
		t.Errorf("got %+v", got)
	}
}

func TestGetAndBody(t *testing.T) {
	res := routertest.Get(newRouter(), "/users/7")
	res.AssertStatus(t, http.StatusOK)
	res.AssertBody(t, "user 7")
}

func TestFormBodyAndHeader(t *testing.T) {
	res := routertest.Do(newRouter(), http.MethodPost, "/login",
		routertest.FormBody(url.Values{"name": {"bo"}}))
	res.AssertStatus(t, http.StatusNoContent)
	res.AssertHeader(t, "X-Who", "bo")
}

func TestNewServer(t *testing.T) {
	srv := routertest.NewServer(t, newRouter())

	res, err := srv.Client().Get(srv.URL + "/users/9")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
}

// eventRouter streams three events, a comment and a retry frame, which covers
// every frame that a client has to read past.
func eventRouter() *router.Router[*appContext] {
	r := router.New(func(http.ResponseWriter, *http.Request) *appContext {
		return new(appContext)
	})
	r.GET("/events", func(c *appContext) error {
		s, err := c.SSE(http.StatusOK, router.SSERetry(2*time.Second))
		if err != nil {
			return err
		}
		if err := s.Send(router.Event{ID: "1", Name: "tick", Data: "one"}); err != nil {
			return err
		}
		if err := s.Comment("ping"); err != nil {
			return err
		}
		// The second event names no id, so it keeps the one before it, and no
		// name, so a client reports it as a message.
		if err := s.Send(router.Event{Data: "two\nlines"}); err != nil {
			return err
		}
		return s.Send(router.Event{ID: "3", Name: "tick", Data: "three"})
	})
	return r
}

func TestEvents(t *testing.T) {
	res := routertest.Get(eventRouter(), "/events")
	res.AssertStatus(t, http.StatusOK)
	res.AssertHeader(t, "Content-Type", "text/event-stream")
	routertest.AssertEvents(t, res,
		routertest.Event{ID: "1", Name: "tick", Data: "one"},
		routertest.Event{ID: "1", Data: "two\nlines"},
		routertest.Event{ID: "3", Name: "tick", Data: "three"},
	)
}

// The parser reads what a client reads, whichever line break the stream uses,
// and whatever else it carries.
func TestEventsParsing(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		want   []routertest.Event
	}{
		{"empty stream", "", nil},
		{"one event", "data: one\n\n", []routertest.Event{{Data: "one"}}},
		{"carriage returns", "event: tick\r\ndata: one\r\n\r\n", []routertest.Event{{Name: "tick", Data: "one"}}},
		{"lone carriage returns", "event: tick\rdata: one\r\r", []routertest.Event{{Name: "tick", Data: "one"}}},
		{"no space after the colon", "data:one\n\n", []routertest.Event{{Data: "one"}}},
		{"one space only", "data:  one\n\n", []routertest.Event{{Data: " one"}}},
		{"comments", ": ping\ndata: one\n: ping\n\n", []routertest.Event{{Data: "one"}}},
		{"retry frame", "retry: 2000\n\ndata: one\n\n", []routertest.Event{{Data: "one"}}},
		{"no data field", "event: tick\n\ndata: one\n\n", []routertest.Event{{Data: "one"}}},
		{"empty data", "event: tick\ndata: \n\n", []routertest.Event{{Name: "tick"}}},
		{"unknown field", "kind: odd\ndata: one\n\n", []routertest.Event{{Data: "one"}}},
		{"field without a colon", "data\ndata: one\n\n", []routertest.Event{{Data: "\none"}}},
		{"an id carries forward", "id: 7\ndata: one\n\ndata: two\n\n", []routertest.Event{{ID: "7", Data: "one"}, {ID: "7", Data: "two"}}},
		{"an id with a NUL", "id: a\x00b\ndata: one\n\n", []routertest.Event{{Data: "one"}}},
		{"an unterminated last frame", "data: one\n\ndata: two\n", []routertest.Event{{Data: "one"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := routertest.Get(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				//nolint:errcheck // The recorder never fails.
				io.WriteString(w, tt.stream)
			}), "/events")

			got := routertest.Events(res)
			if len(got) != len(tt.want) {
				t.Fatalf("%d events, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("event %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
