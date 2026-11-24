package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// JobWaitReport is how often Run names the jobs of Config.Background that
// have not stopped yet, while it waits for them after the cancel.
const JobWaitReport = 2 * time.Second

// jobWaitReport is JobWaitReport, which the tests shorten.
var jobWaitReport = JobWaitReport

// Job is one function that runs beside a server; see Config.Background. Name
// labels it in the lines Run logs and in the timeout error; an empty Name
// takes "job" and its index.
type Job struct {
	Run  func(ctx context.Context) error
	Name string
}

// background is the Config.Background jobs of one server while they run. A
// server with none gets a background whose failed channel is nil, so the
// select of serve never picks it.
type background struct {
	cancel context.CancelFunc
	log    *slog.Logger

	// failed closes on the first error a job reports before stop.
	failed chan struct{}
	once   sync.Once

	mu        sync.Mutex
	stoppedAt time.Time
	errs      []error
	names     []string
	running   []bool
	stopping  bool

	wg sync.WaitGroup
}

// startBackground starts each of jobs on its own goroutine, with a context
// derived from ctx that ends on stop.
func startBackground(ctx context.Context, jobs []Job, log *slog.Logger) *background {
	bg := &background{log: log}
	if len(jobs) == 0 {
		return bg
	}
	bg.failed = make(chan struct{})
	bg.names = make([]string, len(jobs))
	bg.running = make([]bool, len(jobs))
	for i, j := range jobs {
		bg.names[i] = jobName(i, j)
		bg.running[i] = true
	}
	rctx, cancel := context.WithCancel(ctx)
	bg.cancel = cancel
	for i, j := range jobs {
		bg.wg.Go(func() { bg.report(i, j.Run(rctx)) })
	}
	return bg
}

func jobName(i int, j Job) string {
	if j.Name != "" {
		return j.Name
	}
	return "job " + strconv.Itoa(i)
}

// report records the end of job i. Before stop, an error fails the server;
// after it, context.Canceled is a clean stop.
func (bg *background) report(i int, err error) {
	bg.mu.Lock()
	defer bg.mu.Unlock()
	bg.running[i] = false
	if bg.stopping {
		bg.log.Info("serve: job stopped", slog.String("job", bg.names[i]), slog.Duration("took", time.Since(bg.stoppedAt)))
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
	} else if err != nil {
		bg.once.Do(func() { close(bg.failed) })
	}
	if err != nil {
		bg.errs = append(bg.errs, err)
	}
}

// left names the jobs that still run. The caller holds mu.
func (bg *background) left() []string {
	var names []string
	for i, run := range bg.running {
		if run {
			names = append(names, bg.names[i])
		}
	}
	return names
}

func (bg *background) leftNow() []string {
	bg.mu.Lock()
	defer bg.mu.Unlock()
	return bg.left()
}

// stop cancels the jobs and waits for them within timeout; a negative
// timeout waits for none. It reports what the jobs reported, and the names of
// the ones that did not stop in time.
func (bg *background) stop(timeout time.Duration) error {
	if bg.cancel == nil {
		return nil
	}
	bg.mu.Lock()
	bg.stopping = true
	bg.stoppedAt = time.Now()
	left := bg.left()
	bg.mu.Unlock()
	if len(left) > 0 {
		bg.log.Info("serve: stopping jobs", slog.Any("jobs", left))
	}
	bg.cancel()

	var late error
	if timeout >= 0 {
		late = bg.wait(shutdownTimeout(timeout))
	}

	bg.mu.Lock()
	defer bg.mu.Unlock()
	return joinErrors(append(bg.errs, late)...)
}

func (bg *background) wait(timeout time.Duration) error {
	done := make(chan struct{})
	go func() {
		bg.wg.Wait()
		close(done)
	}()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(jobWaitReport)
	defer tick.Stop()
	for {
		select {
		case <-done:
			bg.log.Info("serve: jobs stopped", slog.Duration("took", time.Since(bg.stoppedAt)))
			return nil
		case <-tick.C:
			bg.log.Info("serve: waiting for jobs", slog.Any("jobs", bg.leftNow()), slog.Duration("waited", time.Since(bg.stoppedAt)))
		case <-deadline.C:
			left := bg.leftNow()
			bg.log.Warn("serve: jobs did not stop in time", slog.Any("jobs", left), slog.Duration("timeout", timeout))
			return fmt.Errorf("serve: the background jobs did not stop in %s: %s", timeout, strings.Join(left, ", "))
		}
	}
}
