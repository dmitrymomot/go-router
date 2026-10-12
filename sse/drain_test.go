package sse

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router/internal/drain"
)

func TestServeEndsOnDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		draining := make(chan struct{})
		req := newReq()
		req = req.WithContext(drain.With(req.Context(), draining))
		ch := make(chan string)
		done := make(chan struct{})
		var err error
		var rec *httptest.ResponseRecorder
		go func() {
			defer close(done)
			rec = serve(func(c *tctx) error {
				err = Serve(c, ch, Text[string]("msg"), Close(Event{Name: "bye"}))
				return err
			}, req)
		}()

		synctest.Wait()
		close(draining)
		<-done
		if err != nil {
			t.Fatalf("Serve = %v, want nil on a drain", err)
		}
		if !strings.Contains(rec.Body.String(), "event: bye\n") {
			t.Errorf("body = %q, want the Close event", rec.Body.String())
		}
	})
}

func TestOpenStopsTheHeartbeatOnDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		draining := make(chan struct{})
		req := newReq()
		req = req.WithContext(drain.With(req.Context(), draining))
		rec := serve(func(c *tctx) error {
			s, err := Open(c, http.StatusOK, Heartbeat(time.Second))
			if err != nil {
				return err
			}
			time.Sleep(1500 * time.Millisecond)
			close(draining)
			time.Sleep(5 * time.Second)
			return s.Close()
		}, req)
		if got := strings.Count(rec.Body.String(), ": ping\n\n"); got != 1 {
			t.Errorf("%d heartbeats, want 1, in %q", got, rec.Body.String())
		}
	})
}
