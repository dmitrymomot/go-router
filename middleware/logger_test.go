package middleware_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func loggerRouter(cfg middleware.LoggerConfig) (*router.Router[*appContext], *bytes.Buffer) {
	var buf bytes.Buffer
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	r := newRouter()
	r.Use(middleware.LoggerWithConfig[*appContext](cfg))
	r.GET("/gone", func(*appContext) error { return router.ErrGone })
	r.GET("/boom", func(*appContext) error { return router.ErrInternalServerError })
	r.GET("/ok", func(c *appContext) error { return c.String(http.StatusOK, "fine") })
	r.GET("/health", func(c *appContext) error { return c.NoContent(http.StatusNoContent) })
	return r, &buf
}

func TestLoggerReportsTheStatusOfAFailedHandler(t *testing.T) {
	r, buf := loggerRouter(middleware.LoggerConfig{})

	get(r, "/gone")
	if !strings.Contains(buf.String(), "status=410") {
		t.Errorf("record = %q, want status=410", buf.String())
	}
	if !strings.Contains(buf.String(), "route=/gone") {
		t.Errorf("record = %q, want the route pattern", buf.String())
	}

	buf.Reset()
	get(r, "/ok")
	if !strings.Contains(buf.String(), "status=200") {
		t.Errorf("record = %q, want status=200", buf.String())
	}
}

// The C06 regression: Logger read StatusOf(err), a 500 for a plain error,
// while the error handler answered the client with something else.
func TestLoggerReportsTheStatusTheErrorHandlerWrote(t *testing.T) {
	errLocked := errors.New("the row is locked")

	var buf bytes.Buffer
	calls := 0
	r := newRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.ErrorHandler(func(c *appContext, err error) error {
		calls++
		if errors.Is(err, errLocked) {
			return c.String(http.StatusLocked, "locked")
		}
		return router.DefaultErrorHandler(c, err)
	})
	r.Use(middleware.LoggerWithConfig[*appContext](middleware.LoggerConfig{
		Logger: slog.New(slog.NewTextHandler(&buf, nil)),
	}))
	r.GET("/rows/{id}", func(*appContext) error { return errLocked })

	rec := get(r, "/rows/7")
	if rec.Code != http.StatusLocked || rec.Body.String() != "locked" {
		t.Errorf("answer = %d %q, want 423 %q", rec.Code, rec.Body.String(), "locked")
	}
	for _, want := range []string{"level=WARN", "status=423", "bytes=6"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("record = %q, want %s", buf.String(), want)
		}
	}
	if calls != 1 {
		t.Errorf("the error handler ran %d times, want 1", calls)
	}
}

func TestLoggerReportsACancelledRequestAs499(t *testing.T) {
	r, buf := loggerRouter(middleware.LoggerConfig{})
	r.Logger(slog.New(slog.DiscardHandler))
	r.GET("/gone-away", func(c *appContext) error {
		ctx, cancel := context.WithCancel(c.Request().Context())
		c.SetContext(ctx)
		cancel()
		return ctx.Err()
	})

	get(r, "/gone-away")
	for _, want := range []string{"level=WARN", "status=499", "bytes=0"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("record = %q, want %s", buf.String(), want)
		}
	}
}

func TestLoggerOutsideTimeoutLogsThe503(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var buf bytes.Buffer
		r := newRouter()
		r.Logger(slog.New(slog.DiscardHandler))
		r.Use(
			middleware.LoggerWithConfig[*appContext](middleware.LoggerConfig{
				Logger: slog.New(slog.NewTextHandler(&buf, nil)),
			}),
			middleware.TimeoutWithConfig[*appContext](middleware.TimeoutConfig{Duration: 20 * time.Millisecond}),
		)
		r.GET("/slow", func(c *appContext) error {
			<-c.Done()
			return c.Err()
		})

		if rec := get(r, "/slow"); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
		if !strings.Contains(buf.String(), "status=503") {
			t.Errorf("record = %q, want status=503", buf.String())
		}
	})
}

func TestLoggerSkip(t *testing.T) {
	r, buf := loggerRouter(middleware.LoggerConfig{Skip: skipPath("/health")})

	get(r, "/health")
	if buf.Len() != 0 {
		t.Errorf("skipped path still logged: %q", buf.String())
	}

	get(r, "/ok")
	if buf.Len() == 0 {
		t.Error("the other paths stopped logging")
	}
}

func TestLoggerCustomMessage(t *testing.T) {
	r, buf := loggerRouter(middleware.LoggerConfig{Message: "http"})

	get(r, "/ok")
	if !strings.Contains(buf.String(), `msg=http`) {
		t.Errorf("record = %q, want msg=http", buf.String())
	}
}

func TestLoggerRecordsTheRequestItself(t *testing.T) {
	r, buf := loggerRouter(middleware.LoggerConfig{})

	req := httptest.NewRequest(http.MethodGet, "/ok?token=secret", nil)
	req.Header.Set(router.HeaderUserAgent, "curl/8.0")
	req.Header.Set(router.HeaderReferer, "https://example.com/page")
	do(r, req)

	record := buf.String()
	for _, want := range []string{
		"method=GET",
		"path=/ok",
		"host=example.com",
		"proto=HTTP/1.1",
		"user_agent=curl/8.0",
		"referer=https://example.com/page",
	} {
		if !strings.Contains(record, want) {
			t.Errorf("record = %q, want %s", record, want)
		}
	}
	if strings.Contains(record, "secret") {
		t.Error("the record carries the query string, which is where a token leaks into a log")
	}
	if strings.Contains(record, "route_host") {
		t.Error("a route that answers every host reported a host pattern")
	}
}

func TestLoggerOmitsAHeaderThatTheRequestDoesNotCarry(t *testing.T) {
	r, buf := loggerRouter(middleware.LoggerConfig{})

	get(r, "/ok")
	if strings.Contains(buf.String(), "referer") {
		t.Errorf("record = %q, want no referer field at all", buf.String())
	}
	if strings.Contains(buf.String(), "user_agent") {
		t.Errorf("record = %q, want no user_agent field at all", buf.String())
	}
}

func TestLoggerDisableUserAgent(t *testing.T) {
	r, buf := loggerRouter(middleware.LoggerConfig{DisableUserAgent: true})

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.Header.Set(router.HeaderUserAgent, "curl/8.0")
	do(r, req)

	if strings.Contains(buf.String(), "user_agent") {
		t.Errorf("record = %q, want no user agent", buf.String())
	}
}

func TestLoggerRecordsTheHostThatAnsweredTheRequest(t *testing.T) {
	var buf bytes.Buffer
	r := newRouter()
	r.Use(middleware.LoggerWithConfig[*appContext](middleware.LoggerConfig{
		Logger: slog.New(slog.NewTextHandler(&buf, nil)),
	}))
	r.Host("{tenant}.example.com", func(h *router.Router[*appContext]) {
		h.GET("/", func(c *appContext) error { return c.NoContent(http.StatusOK) })
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "acme.example.com"
	do(r, req)

	record := buf.String()
	if !strings.Contains(record, "host=acme.example.com") {
		t.Errorf("record = %q, want the host of the request", record)
	}
	if !strings.Contains(record, "route_host={tenant}.example.com") {
		t.Errorf("record = %q, want the host pattern that answered", record)
	}
}

func TestLoggerAttrs(t *testing.T) {
	r, buf := loggerRouter(middleware.LoggerConfig{
		Attrs: func(c router.Context, err error) []slog.Attr {
			return []slog.Attr{
				slog.String("tenant", "acme"),
				slog.Bool("failed", err != nil),
			}
		},
	})

	get(r, "/gone")
	if !strings.Contains(buf.String(), "tenant=acme") {
		t.Errorf("record = %q, want the attribute of the application", buf.String())
	}
	if !strings.Contains(buf.String(), "failed=true") {
		t.Errorf("record = %q, want the error to reach Attrs", buf.String())
	}

	buf.Reset()
	get(r, "/ok")
	if !strings.Contains(buf.String(), "failed=false") {
		t.Errorf("record = %q, want a nil error for a request that succeeded", buf.String())
	}
}

func TestLoggerLevelsSelectInfo(t *testing.T) {
	r, buf := loggerRouter(middleware.LoggerConfig{
		ClientErrorLevel: slog.LevelInfo,
		ServerErrorLevel: slog.LevelInfo,
	})

	for _, path := range []string{"/gone", "/boom"} {
		buf.Reset()
		get(r, path)
		if !strings.Contains(buf.String(), "level=INFO") {
			t.Errorf("%s logged as %q, want level=INFO", path, buf.String())
		}
	}
}

func TestLoggerLevelsMoveAtRunTime(t *testing.T) {
	var serverErrors slog.LevelVar
	serverErrors.Set(slog.LevelError)

	r, buf := loggerRouter(middleware.LoggerConfig{ServerErrorLevel: &serverErrors})

	get(r, "/boom")
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("record = %q, want level=ERROR", buf.String())
	}

	serverErrors.Set(slog.LevelDebug)
	buf.Reset()
	get(r, "/boom")
	if !strings.Contains(buf.String(), "level=DEBUG") {
		t.Errorf("record = %q, want level=DEBUG after the level moved", buf.String())
	}
}

// A handler that reads an oversized body itself returns an
// *http.MaxBytesError. Logger inside BodyLimit answers before BodyLimit sees
// it, and the client still gets a 413.
func TestLoggerInsideBodyLimitAnswersAnOversizedBodyWith413(t *testing.T) {
	var buf bytes.Buffer
	r := newRouter()
	r.Use(
		middleware.BodyLimit[*appContext](4),
		middleware.LoggerWithConfig[*appContext](middleware.LoggerConfig{
			Logger: slog.New(slog.NewTextHandler(&buf, nil)),
		}),
	)
	r.POST("/read", func(c *appContext) error {
		_, err := io.ReadAll(c.Request().Body)
		return err
	})

	req := httptest.NewRequest(http.MethodPost, "/read", strings.NewReader("more than four bytes"))
	req.ContentLength = -1
	rec := do(r, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if !strings.Contains(buf.String(), "status=413") {
		t.Errorf("record = %q, want status=413", buf.String())
	}
}
