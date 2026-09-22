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

	// Params counts the host and path parameters together. A route over
	// InlineParamBudget costs one allocation per request.
	Params int
}

// InlineParamBudget is how many parameters a route carries before its values
// need a second allocation. See Route.Params.
const InlineParamBudget = maxInlineParams

func (r *Router[C]) record(reg registration[C], e *hostEntry[C], full string, meta []any) {
	r.top().describe(reg, e, normalizePattern(full), meta)
}

// Routes reports the table as it stands. Registration stays open afterwards.
// It lists routes alone: the 404, 405 and OPTIONS answers that a scope's
// middleware wraps are not in it.
func (r *Router[C]) Routes() []Route { return r.top().collectRoutes() }

// describe records the metadata a route carries, for Routes.
func (r *Router[C]) describe(reg registration[C], e *hostEntry[C], pattern string, meta []any) {
	if len(meta) == 0 {
		return
	}
	host := ""
	if e != nil {
		host = e.pattern
	}
	if r.info == nil {
		r.info = make(map[routeKey]routeInfo)
	}
	r.info[routeKey{host: host, method: reg.method, pattern: pattern}] = routeInfo{meta: meta}
}

type routeKey struct{ host, method, pattern string }

type routeInfo struct{ meta []any }

func (r *Router[C]) collectRoutes() []Route {
	var out []Route
	add := func(host string) func(pattern, method string, params int) {
		return func(pattern, method string, params int) {
			rt := Route{Host: host, Method: method, Pattern: pattern, Params: params}
			if info, ok := r.info[routeKey{host: host, method: method, pattern: pattern}]; ok {
				rt.Meta = slices.Clone(info.meta)
			}
			out = append(out, rt)
		}
	}
	r.eng.tree.walk(add(""))
	if r.eng.hostSet != nil {
		for _, e := range r.eng.hostSet.all {
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
