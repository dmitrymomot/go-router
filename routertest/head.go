package routertest

import (
	"maps"
	"net/http"
	"slices"
	"testing"
)

// AssertHEADMatchesGET checks the one rule every response path in this module
// answers: a HEAD reply carries the status and the headers its GET would carry,
// and no body. Each path decides HEAD for itself, in its own place, so each one
// has to be held to the rule on its own.
//
// Both requests go to the same handler, so a handler that answers from state it
// changes needs a fresh one instead.
func AssertHEADMatchesGET(tb testing.TB, h http.Handler, target string, opts ...RequestOption) {
	tb.Helper()
	get := Do(h, http.MethodGet, target, opts...)
	head := Do(h, http.MethodHead, target, opts...)

	if head.StatusCode != get.StatusCode {
		tb.Errorf("HEAD %s: status = %d, want the %d of the GET", target, head.StatusCode, get.StatusCode)
	}
	if len(head.Body) != 0 {
		tb.Errorf("HEAD %s: body = %q, want none", target, head.Body)
	}
	if !maps.EqualFunc(head.Header, get.Header, slices.Equal) {
		tb.Errorf("HEAD %s: headers = %v, want the %v of the GET", target, head.Header, get.Header)
	}
}
