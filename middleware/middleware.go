// Package middleware holds the middleware that ships with the router.
//
// Each one comes in two forms. X takes only the required arguments, as in
// r.Use(middleware.Recover[*Context]) or r.Use(middleware.BodyLimit[*Context](n)).
// XWithConfig takes the whole config, as in
// r.Use(middleware.RecoverWithConfig(middleware.RecoverConfig[*Context]{})).
//
// A field left at its zero value takes its default. Every Config carries a
// Skip field, and every callback of a Config, Skip included, takes the context
// C of the router. A request for which Skip reports true passes straight to
// the next handler, which suits a health check or an asset path. A nil Skip
// skips nothing.
//
// Both forms check their arguments at the call, so a setting that cannot work,
// such as a negative size, panics at the line that wrote it and never at the
// first request.
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
// HTMXRedirect answers the error of a partial request itself, so that a
// redirect of the error handler is turned too. Like Logger, it goes outside
// Timeout, BodyLimit and Decompress, which replace an error on the way out.
//
// BodyLimit goes before anything that reads the body. ParseForm reads it under
// the cap in force, and it goes after auth and CSRF, so a stranger cannot make
// it read a large body. It goes outside Idempotency, so a form that is too
// large or does not parse is refused before a key is read from it.
//
// RateLimit goes before auth when it counts addresses, and after it when it
// counts accounts.
//
// Idempotency goes inside auth and CSRF, so its Client sees the user and a
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

func skipped[C router.Context](skip func(C) bool, c C) bool {
	return skip != nil && skip(c)
}

// isSafeMethod reports the methods RFC 9110 calls safe: they change nothing,
// so CSRF lets them through without a token and a repeat cannot harm.
func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, router.MethodQuery:
		return true
	}
	return false
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

// checkOrigins reports origins in canonical form, and whether one of them is
// "*". It panics on an entry that is not an origin, and on "*" unless
// wildcard allows it. hint ends the message of that panic.
func checkOrigins(setting string, origins []string, wildcard bool, hint string) (out []string, star bool) {
	out = make([]string, len(origins))
	for i, o := range origins {
		if o == "*" && wildcard {
			out[i], star = o, true
			continue
		}
		canonical, ok := originOf(o)
		if !ok {
			panic("middleware: " + setting + " got " + strconv.Quote(o) +
				`, which is not an origin; write a scheme and a host, as in "https://app.example"` + hint)
		}
		out[i] = canonical
	}
	return out, star
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
