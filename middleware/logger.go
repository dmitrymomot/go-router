package middleware

import (
	"log/slog"
	"time"

	"github.com/dmitrymomot/go-router"
)

// LoggerConfig configures [LoggerWithConfig].
type LoggerConfig struct {
	// Skip passes a request straight to the next handler when it returns true.
	// Use it to keep a health check out of the log.
	Skip func(c router.Context) bool

	// Logger receives the records. It defaults to [slog.Default].
	Logger *slog.Logger

	// Level is the level of a record for a status below 400.
	Level slog.Level

	// ClientErrorLevel is the level for a status from 400 to 499. It defaults
	// to [slog.LevelWarn].
	ClientErrorLevel slog.Level

	// ServerErrorLevel is the level for a status of 500 and above. It defaults
	// to [slog.LevelError].
	ServerErrorLevel slog.Level

	// Message is the text of the record. It defaults to "request".
	Message string
}

// Logger is [LoggerWithConfig] with its default config, which writes to
// [slog.Default]. It is a middleware itself, so it goes into Use without a
// call:
//
//	r.Use(middleware.Logger[Ctx])
func Logger[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return LoggerWithConfig[C](LoggerConfig{})(next)
}

// LoggerWithConfig writes one record per request.
//
// It reports the status that the client sees even when the handler returned an
// error, because the error handler runs after this middleware returns and the
// response is still uncommitted at that moment.
func LoggerWithConfig[C router.Context](cfg LoggerConfig) router.Middleware[C] {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ClientErrorLevel == 0 {
		cfg.ClientErrorLevel = slog.LevelWarn
	}
	if cfg.ServerErrorLevel == 0 {
		cfg.ServerErrorLevel = slog.LevelError
	}
	if cfg.Message == "" {
		cfg.Message = "request"
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) {
				return next(c)
			}

			req := c.Request()
			start := time.Now()
			err := next(c)
			status := statusOf(c, err)

			level := cfg.Level
			switch {
			case status >= 500:
				level = cfg.ServerErrorLevel
			case status >= 400:
				level = cfg.ClientErrorLevel
			}

			attrs := []slog.Attr{
				slog.String("method", req.Method),
				slog.String("path", req.URL.Path),
				slog.String("route", c.RoutePattern()),
				slog.Int("status", status),
				slog.Duration("took", time.Since(start)),
				slog.Int64("bytes", c.Response().Size),
				slog.String("ip", ClientIP(c)),
			}
			if id := RequestIDFrom(c); id != "" {
				attrs = append(attrs, slog.String("request_id", id))
			}
			if err != nil {
				attrs = append(attrs, slog.Any("error", err))
			}
			cfg.Logger.LogAttrs(req.Context(), level, cfg.Message, attrs...)
			return err
		}
	}
}
