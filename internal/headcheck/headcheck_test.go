package headcheck_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router/internal/headcheck"
)

// recordingTB keeps what MatchesGET reports instead of failing the test.
type recordingTB struct {
	testing.TB
	errors []string
}

func (tb *recordingTB) Helper() {}

func (tb *recordingTB) Errorf(format string, args ...any) {
	tb.errors = append(tb.errors, fmt.Sprintf(format, args...))
}

func TestMatchesGETAcceptsAHandlerThatKeepsTheRule(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Key", r.Header.Get("X-Key"))
		w.WriteHeader(http.StatusTeapot)
		if r.Method != http.MethodHead {
			//nolint:errcheck // The recorder never fails.
			io.WriteString(w, "body")
		}
	})
	tb := new(recordingTB)
	headcheck.MatchesGET(tb, h, "/x", func(r *http.Request) { r.Header.Set("X-Key", "k") })
	if len(tb.errors) != 0 {
		t.Errorf("errors = %q, want none", tb.errors)
	}
}

func TestMatchesGETReportsEveryBreak(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("X-Head", "1")
			w.WriteHeader(http.StatusNoContent)
		}
		//nolint:errcheck // The recorder never fails.
		io.WriteString(w, "body")
	})
	tb := new(recordingTB)
	headcheck.MatchesGET(tb, h, "/x")
	want := []string{"status = 204", "body = ", "headers = "}
	if len(tb.errors) != len(want) {
		t.Fatalf("errors = %q, want %d", tb.errors, len(want))
	}
	for i, w := range want {
		if !strings.Contains(tb.errors[i], "HEAD /x: "+w) {
			t.Errorf("error %d = %q, want one with %q", i, tb.errors[i], w)
		}
	}
}
