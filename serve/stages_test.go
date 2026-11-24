package serve_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router/serve"
)

type stageLine struct {
	Msg    string   `json:"msg"`
	Level  string   `json:"level"`
	Server string   `json:"server"`
	Job    string   `json:"job"`
	Jobs   []string `json:"jobs"`
}

func stageLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

func stageLines(tb testing.TB, buf *bytes.Buffer) []stageLine {
	tb.Helper()
	var lines []stageLine
	for raw := range strings.Lines(buf.String()) {
		var l stageLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			tb.Fatalf("decode %q: %v", raw, err)
		}
		if strings.HasPrefix(l.Msg, "serve: ") {
			lines = append(lines, l)
		}
	}
	return lines
}

func messages(lines []stageLine) []string {
	msgs := make([]string, len(lines))
	for i, l := range lines {
		msgs[i] = l.Msg
	}
	return msgs
}

func untilDone(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestRunLogsEveryStageOfTheStop(t *testing.T) {
	logger, buf := stageLogger()
	s := start(t, ok(), serve.Config{
		Name:       "public",
		Logger:     logger,
		DrainDelay: time.Millisecond,
		Background: []serve.Job{{Name: "relay", Run: untilDone}, {Name: "cron", Run: untilDone}},
	})
	if err := s.stop(t); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}

	lines := stageLines(t, buf)
	got := messages(lines)
	want := []string{"serve: stopping", "serve: draining", "serve: drained", "serve: stopping jobs", "serve: job stopped", "serve: job stopped", "serve: jobs stopped"}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	for _, l := range lines {
		if l.Server != "public" || l.Level != "INFO" {
			t.Errorf("%q: server %q at %s, want public at INFO", l.Msg, l.Server, l.Level)
		}
	}
	if jobs := lines[3].Jobs; !slices.Equal(jobs, []string{"relay", "cron"}) {
		t.Errorf("stopping jobs names %q, want relay and cron", jobs)
	}
	stopped := []string{lines[4].Job, lines[5].Job}
	slices.Sort(stopped)
	if !slices.Equal(stopped, []string{"cron", "relay"}) {
		t.Errorf("stopped jobs = %q, want cron and relay", stopped)
	}
}

func TestRunNamesTheJobsItWaitsFor(t *testing.T) {
	serve.ShortenJobWaitReport(t, 10*time.Millisecond)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	logger, buf := stageLogger()
	s := start(t, ok(), serve.Config{
		Logger:          logger,
		ShutdownTimeout: 100 * time.Millisecond,
		Background: []serve.Job{
			{Run: untilDone},
			{Name: "stuck", Run: func(context.Context) error {
				<-release
				return nil
			}},
		},
	})
	err := s.stop(t)
	if err == nil || !strings.Contains(err.Error(), "did not stop in 100ms: stuck") {
		t.Fatalf("err = %v, want the timeout naming stuck", err)
	}

	lines := stageLines(t, buf)
	var waited, timedOut bool
	for _, l := range lines {
		switch l.Msg {
		case "serve: job stopped":
			if l.Job != "job 0" {
				t.Errorf("job stopped names %q, want job 0", l.Job)
			}
		case "serve: waiting for jobs":
			waited = slices.Equal(l.Jobs, []string{"stuck"})
		case "serve: jobs did not stop in time":
			timedOut = l.Level == "WARN" && slices.Equal(l.Jobs, []string{"stuck"})
		}
		if l.Server != s.addr {
			t.Errorf("%q names server %q, want the address %s", l.Msg, l.Server, s.addr)
		}
	}
	if !waited {
		t.Errorf("no waiting line named stuck: %q", messages(lines))
	}
	if !timedOut {
		t.Errorf("no warn line named stuck: %q", messages(lines))
	}
}

func TestRunWarnsWhenTheDrainRunsOutOfTime(t *testing.T) {
	b := newBlocker(t)
	logger, buf := stageLogger()
	s := start(t, b, serve.Config{Name: "slow", Logger: logger, ShutdownTimeout: 50 * time.Millisecond})
	res := call(client(t), s.url("http", "/"))
	b.running(t)
	if err := s.stop(t); err == nil {
		t.Fatal("err = nil, want the drain timeout")
	}
	b.free()
	<-res

	lines := stageLines(t, buf)
	got := messages(lines)
	want := []string{"serve: stopping", "serve: draining", "serve: the drain failed"}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	if lines[2].Level != "WARN" {
		t.Errorf("the drain failure logs at %s, want WARN", lines[2].Level)
	}
}
