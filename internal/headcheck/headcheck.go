// Package headcheck holds the rule every response path of this module answers
// to: a HEAD reply carries the status and the headers its GET would carry, and
// no body. Each path decides HEAD for itself, in its own place, so each one has
// to be held to the rule on its own.
//
// It imports only the standard library, so the tests of the router package
// itself can use it too.
package headcheck

import (
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// MatchesGET sends a GET and a HEAD of target to h, each changed by opts, and
// reports every way the HEAD reply breaks the rule.
//
// Both requests go to the same handler, so a handler that answers from state it
// changes needs [Compare] and a fresh handler for each request instead.
func MatchesGET(tb testing.TB, h http.Handler, target string, opts ...func(*http.Request)) {
	tb.Helper()
	answer := func(method string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		for _, opt := range opts {
			opt(req)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	Compare(tb, target, answer(http.MethodGet), answer(http.MethodHead))
}

// Compare reports every way head, the reply to a HEAD of target, breaks the
// rule against get, the reply to its GET.
func Compare(tb testing.TB, target string, get, head *httptest.ResponseRecorder) {
	tb.Helper()
	g, h := get.Result(), head.Result()
	body, _ := io.ReadAll(h.Body)

	if h.StatusCode != g.StatusCode {
		tb.Errorf("HEAD %s: status = %d, want the %d of the GET", target, h.StatusCode, g.StatusCode)
	}
	if len(body) != 0 {
		tb.Errorf("HEAD %s: body = %q, want none", target, body)
	}
	if !maps.EqualFunc(h.Header, g.Header, slices.Equal) {
		tb.Errorf("HEAD %s: headers = %v, want the %v of the GET", target, h.Header, g.Header)
	}
}
