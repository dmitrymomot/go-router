// Package middleware holds the middleware that ships with the router.
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
