package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
)

// DeprecationConfig configures [Deprecation]. Deprecated is required: the
// moment the routes were, or will be, deprecated. Sunset is the moment they
// stop working, and is sent only when set. Link is a URL for the documentation
// of the deprecation, sent only when set.
type DeprecationConfig[C router.Context] struct {
	Skip       func(c C) bool
	Deprecated time.Time
	Sunset     time.Time
	Link       string
}

// Deprecation tells clients that the routes it covers are deprecated. It sets
// the Deprecation header of RFC 9745 (@ and the Unix time of Deprecated), the
// Sunset header of RFC 8594 (an HTTP date) when Sunset is set, and a Link
// header with rel="deprecation" when Link is set.
//
// Deprecation panics on a zero Deprecated, on a Sunset before Deprecated, and
// on a Link that holds a '<', a '>' or a control character.
func Deprecation[C router.Context](cfg DeprecationConfig[C]) router.Middleware[C] {
	if cfg.Deprecated.IsZero() {
		panic("middleware: DeprecationConfig needs a Deprecated time; " +
			"set the moment the routes were deprecated")
	}
	if !cfg.Sunset.IsZero() && cfg.Sunset.Before(cfg.Deprecated) {
		panic("middleware: DeprecationConfig.Sunset is before Deprecated; " +
			"a route cannot stop working before it is deprecated")
	}
	deprecation := "@" + strconv.FormatInt(cfg.Deprecated.Unix(), 10)
	sunset := ""
	if !cfg.Sunset.IsZero() {
		sunset = cfg.Sunset.UTC().Format(http.TimeFormat)
	}
	link := ""
	if cfg.Link != "" {
		if strings.ContainsFunc(cfg.Link, func(r rune) bool { return r == '<' || r == '>' || r < ' ' || r == 0x7f }) {
			panic("middleware: DeprecationConfig.Link holds a character a URL cannot carry in a Link header; " +
				"write a plain URL")
		}
		link = "<" + cfg.Link + `>; rel="deprecation"`
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}
			h := c.Response().Header()
			h.Set("Deprecation", deprecation)
			if sunset != "" {
				h.Set("Sunset", sunset)
			}
			if link != "" {
				h.Add("Link", link)
			}
			return next(c)
		}
	}
}
