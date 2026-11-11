package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The client goes away while a component renders. Case A: the component
// returns ctx.Err(). Case B: it returns an error that only carries the text,
// as a loader that formats ctx.Err() with %v does.
func TestRenderForAClientThatWentAwayIsA499AtDebug(t *testing.T) {
	cases := map[string]func(ctx context.Context) error{
		"the cause is ctx.Err()": func(ctx context.Context) error { return ctx.Err() },
		"the cause is lost": func(ctx context.Context) error {
			return errors.New("load widgets: " + ctx.Err().Error())
		},
	}
	renders := map[string]func(c *tctx, comp Component) error{
		"Render":       func(c *tctx, comp Component) error { return c.Render(http.StatusOK, comp) },
		"RenderStream": func(c *tctx, comp Component) error { return c.RenderStream(http.StatusOK, comp) },
	}
	for rname, render := range renders {
		for cname, fail := range cases {
			t.Run(rname+"/"+cname, func(t *testing.T) {
				sink := captureLogs(t)

				calls := 0
				var got error
				r := newTestRouter()
				r.ErrorHandler(func(c *tctx, err error) error {
					calls++
					return c.Render(StatusOf(err), ComponentFunc(func(context.Context, io.Writer) error { return nil }))
				})
				r.GET("/", func(c *tctx) error {
					ctx, cancel := context.WithCancel(c.Request().Context())
					c.SetContext(ctx)
					got = render(c, ComponentFunc(func(ctx context.Context, _ io.Writer) error {
						cancel()
						return fail(ctx)
					}))
					return got
				})
				do(r, http.MethodGet, "/")

				if calls != 0 {
					t.Errorf("the error handler ran %d times, want 0", calls)
				}
				if status := StatusOf(got); status != statusClientClosedRequest {
					t.Errorf("StatusOf(%v) = %d, want 499", got, status)
				}
				if len(sink.records) != 1 {
					t.Fatalf("logged %d records, want 1", len(sink.records))
				}
				rec := sink.records[0]
				if rec.Level != slog.LevelDebug {
					t.Errorf("level = %v, want DEBUG", rec.Level)
				}
				if status, _ := intAttr(rec, "status"); status != statusClientClosedRequest {
					t.Errorf("status = %d, want 499", status)
				}
			})
		}
	}
}

func TestRenderErrorKeepsTheTextOfALostCause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := NewBase(httptest.NewRecorder(), newRequestWithContext(ctx))
	err := b.renderError(errors.New("load widgets: context canceled"))
	if got, want := err.Error(), "router: render component: load widgets: context canceled"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, context.Canceled) {
		t.Error("errors.Is(err, context.Canceled) = false, want true")
	}
}

func TestRenderErrorOfALiveRequestIsA500(t *testing.T) {
	b := NewBase(httptest.NewRecorder(), newRequestWithContext(context.Background()))
	if got := StatusOf(b.renderError(errors.New("boom"))); got != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", got)
	}
	// A server that gave up is not a client that went away.
	if got := StatusOf(b.renderError(fmt.Errorf("query: %w", context.DeadlineExceeded))); got != http.StatusInternalServerError {
		t.Errorf("status of a deadline = %d, want 500", got)
	}
	// An HTTPError passes through.
	if got := StatusOf(b.renderError(ErrNotFound)); got != http.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}

func TestClientGone(t *testing.T) {
	live := newRequestWithContext(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gone := newRequestWithContext(ctx)
	timedOut, stop := context.WithTimeout(context.Background(), 0)
	defer stop()
	<-timedOut.Done()

	tests := []struct {
		name string
		req  *http.Request
		err  error
		want bool
	}{
		{"a live request with a plain error", live, errors.New("boom"), false},
		{"a live request with context.Canceled", live, fmt.Errorf("load: %w", context.Canceled), true},
		{"a canceled request", gone, errors.New("load: context canceled"), true},
		{"a canceled request with no error", gone, nil, true},
		{"a server deadline", newRequestWithContext(timedOut), context.DeadlineExceeded, false},
	}
	for _, tc := range tests {
		if got := ClientGone(tc.req, tc.err); got != tc.want {
			t.Errorf("%s: ClientGone = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func newRequestWithContext(ctx context.Context) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	return req
}
