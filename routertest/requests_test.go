package routertest_test

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
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
	for rt, req := range routertest.Requests(t, r.Routes(), fill) {
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

	values := map[string]string{"id": "a b/c", "path": "x/y z"}
	fill := func(_ router.Route, param string) string { return values[param] }
	for rt, req := range routertest.Requests(t, r.Routes(), fill) {
		want := values["id"]
		if rt.Pattern == "/files/{path...}" {
			want = values["path"]
		}
		routertest.Serve(r, req).Expect(t).Status(http.StatusOK).Body(want)
	}
}

func TestRequestsFailOnAValueTheParameterRefuses(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		fill    func(router.Route, string) string
	}{
		{"a value the expression refuses", "/n/{n:[0-9]+}", func(router.Route, string) string { return "a b" }},
		{"x for an expression", "/c/{code:[a-z]{2}}", nil},
		{"a value the class refuses", "/int/{id:int}", func(router.Route, string) string { return "one" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := router.New(newContext)
			r.GET(tt.pattern, echoRoute)
			r.GET("/after", echoRoute)

			tb := new(recordingTB)
			var sent []string
			for rt := range routertest.Requests(tb, r.Routes(), tt.fill) {
				sent = append(sent, rt.Pattern)
			}
			if len(tb.fatals) != 1 || !strings.Contains(tb.msg, tt.pattern) {
				t.Fatalf("fatals = %q, want one that names %s", tb.fatals, tt.pattern)
			}
			routes := r.Routes()
			failed := slices.IndexFunc(routes, func(rt router.Route) bool { return rt.Pattern == tt.pattern })
			want := make([]string, failed)
			for i, rt := range routes[:failed] {
				want[i] = rt.Pattern
			}
			if !slices.Equal(sent, want) {
				t.Errorf("sent %q, want %q: the sequence ends at the failure", sent, want)
			}
		})
	}
}

func TestRequestsFillWithX(t *testing.T) {
	r := router.New(newContext)
	r.GET("/users/{id}", echoRoute)
	for _, fill := range []func(router.Route, string) string{
		nil,
		func(router.Route, string) string { return "" },
	} {
		for _, req := range routertest.Requests(t, r.Routes(), fill) {
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

	for _, req := range routertest.Requests(t, r.Routes(), nil) {
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
	for range routertest.Requests(t, r.Routes(), func(rt router.Route, param string) string {
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
	for rt, req := range routertest.Requests(t, r.Routes(), nil) {
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
	for rt, req := range routertest.Requests(t, r.Routes(), nil,
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
	for range routertest.Requests(t, r.Routes(), func(router.Route, string) string {
		calls++
		return ""
	}) {
		break
	}
	if calls != 1 {
		t.Errorf("fill ran %d times, want 1: the second request was built", calls)
	}
}
