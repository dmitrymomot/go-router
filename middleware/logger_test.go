package middleware_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"

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
	r.GET("/ok", func(c *appContext) error { return c.String(http.StatusOK, "fine") })
	r.GET("/health", func(c *appContext) error { return c.NoContent(http.StatusNoContent) })
	return r, &buf
}

func TestLoggerReportsTheStatusOfAFailedHandler(t *testing.T) {
	r, buf := loggerRouter(middleware.LoggerConfig{})

	get(r, "/gone")
	// The handler returned an error, so the response was still uncommitted
	// when the middleware ran. The record must still carry 410.
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
