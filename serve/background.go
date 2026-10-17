package serve

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// background is the Config.Background functions of one server while they
// run. A server with none gets a background whose failed channel is nil, so
// the select of serve never picks it.
type background struct {
	cancel context.CancelFunc

	// failed closes on the first error a function reports before stop.
	failed chan struct{}
	once   sync.Once

	mu       sync.Mutex
	errs     []error
	stopping bool

	wg sync.WaitGroup
}

// startBackground starts each of fns on its own goroutine, with a context
// derived from ctx that ends on stop.
func startBackground(ctx context.Context, fns []func(context.Context) error) *background {
	bg := new(background)
	if len(fns) == 0 {
		return bg
	}
	bg.failed = make(chan struct{})
	rctx, cancel := context.WithCancel(ctx)
	bg.cancel = cancel
	for _, fn := range fns {
		bg.wg.Go(func() { bg.report(fn(rctx)) })
	}
	return bg
}

func (bg *background) report(err error) {
	if err == nil {
		return
	}
	bg.mu.Lock()
	defer bg.mu.Unlock()
	if bg.stopping {
		if errors.Is(err, context.Canceled) {
			return
		}
	} else {
		bg.once.Do(func() { close(bg.failed) })
	}
	bg.errs = append(bg.errs, err)
}

// stop cancels the functions and waits for them within timeout, which takes
// DefaultShutdownTimeout when zero; a negative timeout waits for none. It
// reports what the functions reported, the failure that stopped the server
// first.
func (bg *background) stop(timeout time.Duration) error {
	if bg.cancel == nil {
		return nil
	}
	bg.mu.Lock()
	bg.stopping = true
	bg.mu.Unlock()
	bg.cancel()

	var late error
	if timeout >= 0 {
		if timeout == 0 {
			timeout = DefaultShutdownTimeout
		}
		done := make(chan struct{})
		go func() {
			bg.wg.Wait()
			close(done)
		}()
		t := time.NewTimer(timeout)
		defer t.Stop()
		select {
		case <-done:
		case <-t.C:
			late = fmt.Errorf("serve: the background functions did not stop in %s", timeout)
		}
	}

	bg.mu.Lock()
	defer bg.mu.Unlock()
	return joinErrors(append(bg.errs, late)...)
}
