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
//
// # Order
//
// [router.Router.Use] runs its middleware in the order given, the first one
// outermost. The middleware here fits together in this order, outermost
// first:
//
//  1. [RealIP]
//  2. [RequestID]
//  3. [Logger]
//  4. [Recover]
//  5. [Secure] and [CORS]
//  6. [Gzip]
//  7. [HTMXRedirect]
//  8. [BodyLimit], then [Decompress]
//  9. [RateLimit] and auth: [KeyAuth] or [BasicAuth]
//  10. [CSRF]
//  11. [ParseForm]
//  12. [MinDuration]
//  13. [Idempotency]
//  14. [Timeout]
//  15. the handler
//
// RealIP goes first, so everything after it sees the address and the scheme
// of the client. RequestID goes before Logger, so the line carries the id.
//
// Logger goes outside Recover, so it logs the 500 of a panic. It answers the
// error before it logs, so it also goes outside every middleware that replaces
// an error on the way out: Timeout, BodyLimit and Decompress.
//
// Gzip and HTMXRedirect go outside Idempotency, so a replay is compressed, or
// its redirect turned, for the request that asks again.
//
// BodyLimit goes before anything that reads the body. ParseForm reads it under
// the cap in force, and it goes after auth and CSRF, so a stranger cannot make
// it read a large body. It goes outside Idempotency, so a form that is too
// large or does not parse is refused before a key is read from it.
//
// RateLimit goes before auth when it counts addresses, and after it when it
// counts accounts.
//
// Idempotency goes inside auth and CSRF, so its Scope sees the user and a
// forged request cannot claim a key.
//
// MinDuration goes outside Idempotency, so a replay waits too. It hides only
// what runs inside it: a middleware in front that refuses, such as auth, CSRF
// or RateLimit, answers at once. For a defence against account enumeration,
// put the floor around every branch that depends on the account.
//
// Timeout goes last, so an answer that ran out of time is never stored, and
// the context it ends does not cut the floor of MinDuration short.
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
