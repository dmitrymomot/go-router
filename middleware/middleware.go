// Package middleware holds the middleware that ships with the router.
//
// Each one comes in two forms. X takes only the required arguments, as in
// r.Use(middleware.Recover[*Context]) or r.Use(middleware.BodyLimit[*Context](n)).
// XWithConfig takes the whole config, as in
// r.Use(middleware.RecoverWithConfig(middleware.RecoverConfig[*Context]{})).
// Heartbeat, AllowContentType and Deprecation come in one form alone, and
// StripSlashes and MethodOverride wrap an [http.Handler]; see Order.
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
//  1. [Heartbeat]
//  2. [RealIP]
//  3. [RequestID]
//  4. [Logger]
//  5. [Recover]
//  6. [Secure] and [CORS], with [NoCache] and [Deprecation]
//  7. [Gzip]
//  8. [ETag]
//  9. [HTMXRedirect]
//  10. [Throttle]
//  11. [BodyLimit], then [Decompress] and [AllowContentType]
//  12. [RateLimit] and auth: [KeyAuth] or [BasicAuth]
//  13. [CSRF] or [CrossOrigin]
//  14. [ParseForm]
//  15. [MinDuration]
//  16. [Idempotency]
//  17. [Timeout]
//  18. the handler
//
// [StripSlashes] and [MethodOverride] change the path or the method that picks
// the route, so they wrap the router itself, as in
// http.ListenAndServe(addr, middleware.StripSlashes(r)), rather than go in Use.
//
// Heartbeat goes first, so a probe skips the log, the limits and the rest.
//
// RealIP goes before the rest, so everything after it sees the address and
// the scheme of the client. Secure and CSRF read the scheme through
// router.SchemeOf, which trusts X-Forwarded-Proto as it stands; RealIP is what
// keeps only a trusted proxy's value, so mount it whenever either runs.
// RequestID goes before Logger, so the line carries the id.
//
// Logger goes outside Recover, so it logs the 500 of a panic. It answers the
// error before it logs, so it also goes outside every middleware that replaces
// an error on the way out: Timeout, BodyLimit and Decompress.
//
// Gzip and HTMXRedirect go outside Idempotency, so a replay is compressed, or
// its redirect turned, for the request that asks again.
//
// ETag goes inside Gzip, so it hashes the body the handler wrote and Gzip
// marks the tag of the encoding it sends. NoCache drops the conditional
// headers of the request, so a route under it never answers 304: do not cover
// a route with both.
//
// HTMXRedirect answers the error of a partial request itself, so that a
// redirect of the error handler is turned too. Like Logger, it goes outside
// Timeout, BodyLimit and Decompress, which replace an error on the way out.
//
// Throttle goes after Logger and Recover, so a request it turns away is
// logged, and before the body is read and auth runs, so the slots it counts
// cover that work too.
//
// BodyLimit goes before anything that reads the body, and AllowContentType
// refuses a body of the wrong type before anything reads it. ParseForm reads
// the body under the cap in force, and it goes after auth and CSRF, so a
// stranger cannot make it read a large body. It goes outside Idempotency, so a form that is too
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
	"slices"
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

// originOf reports the canonical form of an origin: the scheme and the host in
// lower case, and no port when it is the default one of the scheme, so that
// "HTTPS://App.Example:443" and "https://app.example" compare equal. It reports
// false for anything that is not an origin.
func originOf(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Opaque != "" || u.Host == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		strings.Contains(u.Host, "*") {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if port == "" || scheme == "https" && port == "443" || scheme == "http" && port == "80" {
		return scheme + "://" + host, true
	}
	return scheme + "://" + host + ":" + port, true
}

// sameOrigin reports whether the Origin header value sent equals one of the
// canonical origins in list. A value that is not an origin, such as "null",
// matches none.
func sameOrigin(list []string, sent string) bool {
	origin, ok := originOf(sent)
	return ok && slices.Contains(list, origin)
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
