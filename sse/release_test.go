package sse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router"
)

// lateWriter counts the writes that reach it once returned is set.
type lateWriter struct {
	*httptest.ResponseRecorder
	returned atomic.Bool
	late     atomic.Int32
}

func (w *lateWriter) Write(p []byte) (int, error) {
	if w.returned.Load() {
		w.late.Add(1)
	}
	return w.ResponseRecorder.Write(p)
}

func (w *lateWriter) Flush() {
	if w.returned.Load() {
		w.late.Add(1)
	}
	w.ResponseRecorder.Flush()
}

func TestOpenStopsTheHeartbeatWhenTheHandlerReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := router.NewPooled(func() *tctx { return new(tctx) }, func(*tctx) {})
		r.GET("/events", func(c *tctx) error {
			if _, err := Open(c, http.StatusOK, Heartbeat(time.Second)); err != nil {
				return err
			}
			time.Sleep(1500 * time.Millisecond)
			return nil // No Close.
		})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		w := &lateWriter{ResponseRecorder: httptest.NewRecorder()}
		r.ServeHTTP(w, newReq().WithContext(ctx))
		w.returned.Store(true)

		time.Sleep(5 * time.Second)
		synctest.Wait()
		if n := w.late.Load(); n != 0 {
			t.Errorf("%d writes after the handler returned, want none", n)
		}
		if got := strings.Count(w.Body.String(), ": ping\n\n"); got != 1 {
			t.Errorf("%d heartbeats, want 1, in %q", got, w.Body.String())
		}
	})
}
