package routertest_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/routertest"
	"github.com/dmitrymomot/go-router/sse"
)

func TestEventsCarryTheirRetry(t *testing.T) {
	r := router.New(newContext)
	r.GET("/", func(c *appContext) error {
		s, err := sse.Open(c, http.StatusOK, sse.Retry(2*time.Second))
		if err != nil {
			return err
		}
		if err := s.Send(sse.Event{Data: "one", Retry: 1500 * time.Millisecond}); err != nil {
			return err
		}
		return s.Send(sse.Event{Name: "tick", Data: "two"})
	})

	routertest.Get(r, "/").Expect(t).Events(
		routertest.Event{Data: "one", Retry: 1500 * time.Millisecond},
		routertest.Event{Name: "tick", Data: "two"},
	)
}

func TestEventsParsesRetry(t *testing.T) {
	for _, tc := range []struct {
		name, stream string
		want         time.Duration
	}{
		{"milliseconds", "retry: 250\ndata: x\n\n", 250 * time.Millisecond},
		{"after the data", "data: x\nretry: 250\n\n", 250 * time.Millisecond},
		{"not a number", "retry: soon\ndata: x\n\n", 0},
		{"negative", "retry: -5\ndata: x\n\n", 0},
		{"a block without data", "retry: 250\n\ndata: x\n\n", 0},
	} {
		res := routertest.Recorded(recorded(tc.stream))
		got := routertest.Events(res)
		if len(got) != 1 || got[0].Retry != tc.want {
			t.Errorf("%s: events = %+v, want one with Retry %v", tc.name, got, tc.want)
		}
	}
}

// liveRouter streams n numbered events, then holds the connection open until
// the client leaves.
func liveRouter(n int) *router.Router[*appContext] {
	r := router.New(newContext)
	r.GET("/live", func(c *appContext) error {
		if c.Request().Header.Get("X-Token") == "no" {
			return router.ErrForbidden
		}
		s, err := sse.Open(c, http.StatusOK)
		if err != nil {
			return err
		}
		for i := range n {
			if err := s.Send(sse.Event{ID: string(rune('a' + i)), Name: "tick", Data: strings.Repeat("x", i+1)}); err != nil {
				return err
			}
		}
		<-c.Request().Context().Done()
		return nil
	})
	return r
}

func TestReadEventsStopsAtTheCount(t *testing.T) {
	srv := routertest.NewServer(t, liveRouter(5))

	got := routertest.ReadEvents(t, srv, "/live", 3)
	want := []routertest.Event{
		{ID: "a", Name: "tick", Data: "x"},
		{ID: "b", Name: "tick", Data: "xx"},
		{ID: "c", Name: "tick", Data: "xxx"},
	}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("ReadEvents = %+v, want %+v", got, want)
	}

	if got := routertest.ReadEvents(t, srv, "/live", 0); len(got) != 0 {
		t.Errorf("ReadEvents of 0 = %+v, want none", got)
	}
}

func TestReadEventsFailsWhenTheStreamStalls(t *testing.T) {
	srv := routertest.NewServer(t, liveRouter(2))

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	tb := new(recordingTB)
	routertest.ReadEvents(tb, srv, "/live", 3, routertest.Context(ctx))
	if len(tb.fatals) != 1 || !strings.Contains(tb.fatals[0], "read 2 of 3 events") {
		t.Fatalf("fatals = %q, want one that says it read 2 of 3 events", tb.fatals)
	}
}

func TestReadEventsFailsWhenTheStreamEnds(t *testing.T) {
	srv := routertest.NewServer(t, eventRouter())

	tb := new(recordingTB)
	routertest.ReadEvents(tb, srv, "/events", 4)
	if len(tb.fatals) != 1 || !strings.Contains(tb.fatals[0], "read 3 of 4 events: EOF") {
		t.Errorf("fatals = %q, want one that says the stream ended after 3 of 4", tb.fatals)
	}
}

func TestReadEventsFailsOnABadStatusAndABadCount(t *testing.T) {
	srv := routertest.NewServer(t, liveRouter(1))

	tb := new(recordingTB)
	routertest.ReadEvents(tb, srv, "/live", 1, routertest.Header("X-Token", "no"))
	if len(tb.fatals) != 1 || !strings.Contains(tb.fatals[0], "status = 403") {
		t.Errorf("fatals = %q, want one that names status 403", tb.fatals)
	}

	tb = new(recordingTB)
	routertest.ReadEvents(tb, srv, "/live", -1)
	if len(tb.fatals) != 1 || !strings.Contains(tb.fatals[0], "-1") {
		t.Errorf("fatals = %q, want one that names the bad count", tb.fatals)
	}

	tb = new(recordingTB)
	routertest.ReadEvents(tb, nil, "/never", 1)
	if len(tb.fatals) != 1 {
		t.Errorf("fatals = %q, want one for a missing server", tb.fatals)
	}
}

// recorded is a recorder that holds body.
func recorded(body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	_, _ = rec.WriteString(body)
	return rec
}
