package router

import (
	"slices"
	"strings"
)

// Route is one entry of the table that [Router.Routes] reports.
type Route struct {
	// Method is "*" for a route of [Router.Any] and for a mount.
	Method  string
	Pattern string
	Host    string

	// Meta holds the values of every [Router.Meta] scope around the route,
	// outermost first. Routes hands out a copy.
	Meta []any
}

// Routes reports the table as it stands. Registration stays open afterwards.
// It lists routes alone: the 404, 405 and OPTIONS answers that a scope's
// middleware wraps are not in it.
func (r *Router[C]) Routes() []Route {
	eng := r.top().eng
	var out []Route
	add := func(host string) func(n *node[C]) {
		return func(n *node[C]) {
			for _, mh := range n.routes {
				out = append(out, Route{Host: host, Method: mh.method, Pattern: n.rec.pattern, Meta: slices.Clone(mh.meta)})
			}
		}
	}
	eng.tree.walk(add(""))
	if eng.hostSet != nil {
		for _, e := range eng.hostSet.all {
			e.tree.walk(add(e.pattern))
		}
	}
	slices.SortFunc(out, func(a, b Route) int {
		if c := strings.Compare(a.Host, b.Host); c != 0 {
			return c
		}
		if c := strings.Compare(a.Pattern, b.Pattern); c != 0 {
			return c
		}
		return strings.Compare(a.Method, b.Method)
	})
	return out
}
