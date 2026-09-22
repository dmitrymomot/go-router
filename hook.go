package router

import (
	"net/url"
	"strings"

	"github.com/dmitrymomot/go-router/internal/routerhook"
)

// The hooks give package routertest what a context built outside a router
// lacks, without a public API that only a test would call.
func init() {
	routerhook.SetRoute = func(b any, pattern string, names, vals []string) {
		b.(*Base).setTestRoute(pattern, names, vals)
	}
	routerhook.FillPattern = fillPattern
}

// setTestRoute gives b a route pattern and its parameters, as routing a
// request would. names and vals pair up by index. An empty pattern leaves b
// with no route.
func (b *Base) setTestRoute(pattern string, names, vals []string) {
	var rec *routeRecord
	if pattern != "" {
		rec = &routeRecord{pattern: pattern}
	}
	b.needsCleanup = true
	b.setRoute(rec, names, vals)
}

// fillPattern writes pattern with each parameter set to what value reports,
// through the same parts that Expand writes, and checks nothing.
func fillPattern(pattern string, host bool, value func(name, constraint string) string) (string, []string) {
	var (
		b     strings.Builder
		pairs []string
	)
	for _, p := range parseURLTemplate(pattern) {
		if p.name == "" {
			b.WriteString(p.lit)
			continue
		}
		v := value(p.name, p.constraint)
		pairs = append(pairs, p.name, v)
		switch {
		case host:
			b.WriteString(v)
		case p.rest:
			b.WriteString(escapeRest(v))
		default:
			b.WriteString(url.PathEscape(v))
		}
	}
	return b.String(), pairs
}
