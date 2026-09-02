// Package middleware holds the middleware that ships with the router.
//
// Each one comes in two forms. The plain form takes the defaults: pass it to
// [router.Router.Use] as it stands, as in r.Use(middleware.Recover[*Context]).
// The WithConfig form takes a Config struct and reports the middleware, as in
// r.Use(middleware.RecoverWithConfig[*Context](cfg)).
//
// Every Config carries a Skip field. A request for which Skip reports true
// passes straight to the next handler, which suits a health check or an asset
// path. A nil Skip skips nothing.
//
// A WithConfig form validates its configuration at the call, so a setting that
// cannot work panics at the line that wrote it and never at the first request.
package middleware

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dmitrymomot/go-router"
)

func skipped[C router.Context](skip func(router.Context) bool, c C) bool {
	return skip != nil && skip(c)
}

func originOf(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Opaque != "" || u.Host == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		strings.Contains(u.Host, "*") {
		return "", false
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), true
}

func checkOrigin(setting, s, hint string) string {
	canonical, ok := originOf(s)
	if !ok {
		panic("middleware: " + setting + " got " + strconv.Quote(s) +
			`, which is not an origin; write a scheme and a host, as in "https://app.example"` + hint)
	}
	return canonical
}

func tooLarge(err error, message string, limit int64) error {
	if _, ok := errors.AsType[*http.MaxBytesError](err); !ok {
		return err
	}
	if _, named := errors.AsType[*router.HTTPError](err); named {
		return err
	}
	return router.ErrPayloadTooLarge.WithMessage(message, limit).WithError(err)
}
