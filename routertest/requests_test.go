package routertest_test

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
)

// echoRoute answers with the host pattern and the path pattern of its route.
func echoRoute(c *appContext) error {
	return c.String(http.StatusOK, c.RouteHost()+" "+c.RoutePattern())
}

// everyShape registers every kind of pattern the router takes. A mount answers
// with its pattern alone, since a plain handler cannot read the host pattern.
func everyShape() *router.Router[*appContext] {
	mount := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "mount "+r.Pattern)
	})
	r := router.New(newContext)
	r.GET("/", echoRoute)
	r.GET("/users/{id}", echoRoute)
	r.GET("/n/{n:[0-9]+}", echoRoute)
	r.GET("/c/{code:[a-z]{2}}", echoRoute)
	r.GET("/int/{id:int}", echoRoute)
	r.GET("/uuid/{id:uuid}", echoRoute)
	r.GET("/slug/{s:slug}", echoRoute)
	r.GET("/reports/rep-{date}.csv", echoRoute)
	r.GET("/files/{path...}", echoRoute)
	r.POST("/users", echoRoute)
	r.Any("/any", echoRoute)
	r.MountHandler("/static", mount)
	r.Host("{tenant}.example.test", func(h *router.Router[*appContext]) {
		h.GET("/t/{id}", echoRoute)
	})
	r.Host("*.wild.test", func(h *router.Router[*appContext]) {
		h.MountHandler("/assets", mount)
	})
	r.Host("{sub...}.deep.test", func(h *router.Router[*appContext]) {
		h.GET("/d", echoRoute)
	})
	r.Host("*", func(h *router.Router[*appContext]) {
		h.GET("/anyhost", echoRoute)
	})
	return r
}

func TestRequestsReachEveryRoute(t *testing.T) {
	r := everyShape()
	fill := func(rt router.Route, param string) string {
		switch param {
		case "n":
			return "42"
		case "code":
			return "ab"
		}
		return ""
	}
	count := 0
	for rt, req := range routertest.Requests(r.Routes(), fill) {
		count++
		t.Run(rt.Method+" "+rt.Host+" "+rt.Pattern, func(t *testing.T) {
			want := rt.Host + " " + rt.Pattern
			if rt.Method == "*" && rt.Pattern != "/any" {
				want = "mount " + rt.Pattern
			}
			routertest.Serve(r, req).Expect(t).Status(http.StatusOK).Body(want)
		})
	}
	if count != len(r.Routes()) {
		t.Errorf("%d requests for %d routes", count, len(r.Routes()))
	}
}

func TestRequestsEscapeTheValues(t *testing.T) {
	r := router.New(newContext)
	r.GET("/users/{id}", func(c *appContext) error { return c.String(http.StatusOK, c.Param("id")) })
	r.GET("/files/{path...}", func(c *appContext) error { return c.String(http.StatusOK, c.Param("path")) })
	r.GET("/n/{n:[0-9]+}", func(c *appContext) error { return c.String(http.StatusOK, c.Param("n")) })

	values := map[string]string{"id": "a b/c", "path": "x/y z", "n": "a b"}
	fill := func(_ router.Route, param string) string { return values[param] }
	for rt, req := range routertest.Requests(r.Routes(), fill) {
		res := routertest.Serve(r, req)
		switch rt.Pattern {
		case "/users/{id}":
			res.Expect(t).Status(http.StatusOK).Body("a b/c")
		case "/files/{path...}":
			res.Expect(t).Status(http.StatusOK).Body("x/y z")
		default:
			// Expand refuses a value the constraint does not match, so the
			// value goes out escaped by hand and misses the route.
			if got := req.URL.EscapedPath(); got != "/n/a%20b" {
				t.Errorf("path = %q, want /n/a%%20b", got)
			}
			res.Expect(t).Status(http.StatusNotFound)
		}
	}
}

func TestRequestsFillWithX(t *testing.T) {
	r := router.New(newContext)
	r.GET("/users/{id}", echoRoute)
	for _, fill := range []func(router.Route, string) string{
		nil,
		func(router.Route, string) string { return "" },
	} {
		for _, req := range routertest.Requests(r.Routes(), fill) {
			if req.URL.Path != "/users/x" {
				t.Errorf("path = %q, want /users/x", req.URL.Path)
			}
		}
	}
}

func TestRequestsFillTheBuiltinClasses(t *testing.T) {
	r := router.New(newContext)
	r.GET("/int/{id:int}", echoRoute)
	r.GET("/uuid/{id:uuid}", echoRoute)
	r.GET("/slug/{s:slug}", echoRoute)

	for _, req := range routertest.Requests(r.Routes(), nil) {
		routertest.Serve(r, req).Expect(t).Status(http.StatusOK)
	}
}

func TestRequestsAskFillForEachParameter(t *testing.T) {
	r := router.New(newContext)
	r.GET("/r/rep-{date}.csv", echoRoute)
	r.Host("{tenant}.example.test", func(h *router.Router[*appContext]) {
		h.GET("/users/{id}", echoRoute)
	})
	r.Host("*.wild.test", func(h *router.Router[*appContext]) {
		h.MountHandler("/static", http.NotFoundHandler())
	})

	var asked []string
	sent := 0
	for range routertest.Requests(r.Routes(), func(rt router.Route, param string) string {
		asked = append(asked, rt.Pattern+" "+param)
		return ""
	}) {
		sent++
	}
	if sent != 4 {
		t.Errorf("%d requests, want 4", sent)
	}
	slices.Sort(asked)
	want := []string{
		"/r/rep-{date}.csv date",
		"/static/{*...} *",
		"/users/{id} id",
		"/users/{id} tenant",
	}
	if !slices.Equal(asked, want) {
		t.Errorf("asked %q, want %q", asked, want)
	}
}

func TestRequestsSendAnyAsGET(t *testing.T) {
	r := router.New(newContext)
	r.Any("/any", echoRoute)
	for rt, req := range routertest.Requests(r.Routes(), nil) {
		if rt.Method != "*" || req.Method != http.MethodGet {
			t.Errorf("route method %q went out as %q, want * as GET", rt.Method, req.Method)
		}
	}
}

func TestRequestsApplyTheOptions(t *testing.T) {
	r := router.New(newContext)
	r.GET("/a", echoRoute)
	r.Host("{tenant}.example.test", func(h *router.Router[*appContext]) {
		h.GET("/b", echoRoute)
	})
	for rt, req := range routertest.Requests(r.Routes(), nil,
		routertest.Header("X-Key", "k"), routertest.Host("acme.example.test")) {
		if req.Header.Get("X-Key") != "k" {
			t.Errorf("%s: the header of the option is missing", rt.Pattern)
		}
		if req.Host != "acme.example.test" {
			t.Errorf("%s: host = %q, want the host of the option", rt.Pattern, req.Host)
		}
	}
}

func TestRequestsStopWhenTheLoopBreaks(t *testing.T) {
	r := router.New(newContext)
	r.GET("/a/{id}", echoRoute)
	r.GET("/b/{id}", echoRoute)
	calls := 0
	for range routertest.Requests(r.Routes(), func(router.Route, string) string {
		calls++
		return ""
	}) {
		break
	}
	if calls != 1 {
		t.Errorf("fill ran %d times, want 1: the second request was built", calls)
	}
}
