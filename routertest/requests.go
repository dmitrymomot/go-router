package routertest

import (
	"iter"
	"net/http"
	"slices"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/internal/routerhook"
)

// anyMethod is what Route.Method reports for a route of Router.Any and for a
// mount.
const anyMethod = "*"

// uuidValue is the value a {name:uuid} parameter gets when fill gives none.
const uuidValue = "123e4567-e89b-42d3-a456-426614174000"

// Requests yields one request for each route, with every named host and path
// parameter filled in, such as for a test that every route asks for a key.
//
// fill gives the value of the parameter param of rt. When fill is nil or gives
// "", a parameter of the class int gets "1", one of the class uuid gets a
// canonical UUID, and any other gets "x". An anonymous * label of a host gets
// "x" and never reaches fill. The path is built with [router.Expand], which
// escapes the values; a {name...} keeps its slashes, and so does the rest of
// the path under a mount, which reaches fill as "*". Host values go in as they
// are.
//
// A route of Router.Any or a mount goes out as a GET. The host of the route
// goes in before opts, so opts can override it.
//
// A value that its parameter refuses, such as "x" for a regular expression or
// a class that the router declares, stops the test on tb and ends the
// sequence: the request would reach another route or a 404. Give fill a value
// for it. A value that a static sibling claims, such as "new" next to
// /users/new, is not caught, and reaches the sibling.
func Requests(
	tb testing.TB,
	routes []router.Route,
	fill func(rt router.Route, param string) string,
	opts ...RequestOption,
) iter.Seq2[router.Route, *http.Request] {
	opts = slices.Clone(opts)
	return func(yield func(router.Route, *http.Request) bool) {
		tb.Helper()
		for _, rt := range routes {
			value := func(name, class string) string {
				if fill != nil {
					if v := fill(rt, name); v != "" {
						return v
					}
				}
				switch class {
				case "int":
					return "1"
				case "uuid":
					return uuidValue
				}
				return "x"
			}
			var reqOpts []RequestOption
			if rt.Host != "" {
				host, _ := routerhook.FillPattern(rt.Host, true, func(name, class string) string {
					if name == "*" {
						return "x"
					}
					return value(name, class)
				})
				reqOpts = append(reqOpts, Host(host))
			}
			path, pairs := routerhook.FillPattern(rt.Pattern, false, value)
			// Expand has no name for the rest of the path under a mount, so
			// that path goes out as FillPattern escaped it.
			if !isMount(pairs) {
				expanded, err := router.Expand(rt.Pattern, pairs...)
				if err != nil {
					tb.Fatalf("routertest: Requests cannot fill %s %s: %v; give fill a value the parameter takes",
						rt.Method, rt.Pattern, err)
					return
				}
				path = expanded
			}
			method := rt.Method
			if method == anyMethod {
				method = http.MethodGet
			}
			if !yield(rt, Request(method, path, append(reqOpts, opts...)...)) {
				return
			}
		}
	}
}

// isMount reports whether pairs, as FillPattern lists them, name the rest of
// the path under a mount.
func isMount(pairs []string) bool {
	for i := 0; i < len(pairs); i += 2 {
		if pairs[i] == "*" {
			return true
		}
	}
	return false
}
