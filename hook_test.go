package router

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"testing"

	"github.com/dmitrymomot/go-router/internal/routerhook"
)

func TestFillPatternWritesEveryPartAndKeepsAnUnbalancedBrace(t *testing.T) {
	value := func(name, constraint string) string { return "<" + name + ":" + constraint + ">" }
	tests := []struct {
		pattern string
		host    bool
		want    string
	}{
		{pattern: "/a/{id:int}/{rest...}", want: "/a/%3Cid:int%3E/%3Crest:%3E"},
		{pattern: "/r/rep-{d:[0-9]{4}}.csv", want: "/r/rep-%3Cd:%5B0-9%5D%7B4%7D%3E.csv"},
		{pattern: "/files/{*...}", want: "/files/%3C%2A:%3E"},
		{pattern: "/a/{id", want: "/a/{id"},
		{pattern: "/a/x{id}{rest", want: "/a/x%3Cid:%3E{rest"},
		{pattern: "{t.example.test", host: true, want: "{t.example.test"},
		{pattern: "*.{t:slug}.test", host: true, want: "<*:>.<t:slug>.test"},
	}
	for _, tt := range tests {
		if got, _ := fillPattern(tt.pattern, tt.host, value); got != tt.want {
			t.Errorf("fillPattern(%q) = %q, want %q", tt.pattern, got, tt.want)
		}
	}
}

func TestOnHandlerDoneRunsBeforeTheErrorHandlerInReverse(t *testing.T) {
	var got []string
	r := newPooledRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.ErrorHandler(func(c *pctx, err error) error {
		got = append(got, "error handler")
		return c.NoContent(http.StatusTeapot)
	})
	r.GET("/hook", func(c *pctx) error {
		routerhook.OnHandlerDone(&c.Base, func() { got = append(got, "first") })
		routerhook.OnHandlerDone(&c.Base, func() { got = append(got, "second") })
		return errors.New("boom")
	})
	r.GET("/plain", func(c *pctx) error { return c.NoContent(http.StatusNoContent) })
	r.GET("/panic", func(c *pctx) error {
		routerhook.OnHandlerDone(&c.Base, func() { got = append(got, "panic") })
		panic("boom")
	})

	do(r, http.MethodGet, "/hook")
	if want := []string{"second", "first", "error handler"}; !slices.Equal(got, want) {
		t.Errorf("order = %q, want %q", got, want)
	}

	// The pooled context comes back without the functions of the last request.
	got = nil
	do(r, http.MethodGet, "/plain")
	if got != nil {
		t.Errorf("a later request ran %q", got)
	}

	got = nil
	do(r, http.MethodGet, "/panic")
	if want := []string{"panic", "error handler"}; !slices.Equal(got, want) {
		t.Errorf("order after a panic = %q, want %q", got, want)
	}
}
