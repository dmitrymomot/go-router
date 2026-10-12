package sse

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// deadlineLog records every write deadline the stream sets.
type deadlineLog struct {
	*httptest.ResponseRecorder
	mu        sync.Mutex
	deadlines []time.Time
}

func (w *deadlineLog) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadlines = append(w.deadlines, t)
	return nil
}

func TestWriteTimeout(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
		want time.Duration // zero: no deadline after the first clear
	}{
		{"default", nil, DefaultWriteTimeout},
		{"explicit", []Option{WriteTimeout(5 * time.Second)}, 5 * time.Second},
		{"negative disables", []Option{WriteTimeout(-1)}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The clock of the bubble stands still, so the deadline is exact.
			synctest.Test(t, func(t *testing.T) {
				w := &deadlineLog{ResponseRecorder: httptest.NewRecorder()}
				before := time.Now()
				serveTo(open(func(s *Writer) error {
					return s.SendData("x")
				}, tt.opts...), newReq(), w)

				if len(w.deadlines) == 0 || !w.deadlines[0].IsZero() {
					t.Fatalf("deadlines = %v, want the first to clear the deadline", w.deadlines)
				}
				rest := w.deadlines[1:]
				if tt.want == 0 {
					if len(rest) != 0 {
						t.Fatalf("deadlines after the clear = %v, want none", rest)
					}
					return
				}
				if len(rest) == 0 {
					t.Fatal("no write deadline was set")
				}
				for _, d := range rest {
					if got := d.Sub(before); got != tt.want {
						t.Errorf("deadline in %s, want %s", got, tt.want)
					}
				}
			})
		})
	}
}

func TestHeartbeatSetsTheDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &deadlineLog{ResponseRecorder: httptest.NewRecorder()}
		serveTo(open(func(s *Writer) error {
			time.Sleep(2500 * time.Millisecond)
			return s.Close()
		}, Heartbeat(time.Second), WriteTimeout(time.Minute)), newReq(), w)

		// One clear, one for the header and one for each of two beats.
		if got := len(w.deadlines); got != 4 {
			t.Errorf("%d deadline calls, want 4", got)
		}
	})
}

func TestOpenHeartbeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := httptest.NewRecorder()
		serveTo(open(func(s *Writer) error {
			time.Sleep(3500 * time.Millisecond)
			return s.Close()
		}, Heartbeat(time.Second)), newReq(), rec)

		if got := strings.Count(rec.Body.String(), ": ping\n\n"); got != 3 {
			t.Errorf("%d heartbeats, want 3, in %q", got, rec.Body.String())
		}
		// synctest.Test fails if the heartbeat goroutine outlives Close.
	})
}

func TestOpenHeartbeatStopsWithTheRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		req := newReq().WithContext(ctx)
		rec := httptest.NewRecorder()
		serveTo(open(func(*Writer) error {
			time.Sleep(1500 * time.Millisecond)
			cancel()
			time.Sleep(5 * time.Second)
			return nil
		}, Heartbeat(time.Second)), req, rec)
		synctest.Wait()

		if got := strings.Count(rec.Body.String(), ": ping\n\n"); got != 1 {
			t.Errorf("%d heartbeats, want 1 before the request ended", got)
		}
	})
}

func TestWriterCloseSendsTheCloseEvent(t *testing.T) {
	rec := serve(open(func(s *Writer) error {
		if err := s.Close(); err != nil {
			return err
		}
		if err := s.Close(); err != nil {
			t.Errorf("second Close = %v, want nil", err)
		}
		if !s.Closed() {
			t.Error("Closed = false after Close")
		}
		if err := s.SendData("late"); !errors.Is(err, ErrClosed) {
			t.Errorf("Send after Close = %v, want ErrClosed", err)
		}
		return nil
	}, Close(Event{Name: "done", Data: "bye"})), newReq())

	if got, want := rec.Body.String(), "event: done\ndata: bye\n\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestWriterIsSafeForConcurrentUse(t *testing.T) {
	rec := serve(open(func(s *Writer) error {
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				for range 50 {
					_ = s.SendData("x")
					_ = s.Comment("c")
				}
			})
		}
		wg.Wait()
		return s.Close()
	}, Heartbeat(time.Millisecond)), newReq())

	for frame := range strings.SplitSeq(strings.TrimSuffix(rec.Body.String(), "\n\n"), "\n\n") {
		if frame != "data: x" && frame != ": c" && frame != ": ping" {
			t.Fatalf("interleaved frame %q", frame)
		}
	}
}

func TestHeadHasNoHeartbeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		req := newReq()
		req.Method = http.MethodHead
		rec := httptest.NewRecorder()
		serveTo(open(func(s *Writer) error {
			time.Sleep(5 * time.Second)
			return s.Close()
		}, Heartbeat(time.Second)), req, rec)
		if rec.Body.Len() != 0 {
			t.Errorf("body = %q, want empty", rec.Body.String())
		}
	})
}
