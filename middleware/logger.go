package middleware

import (
	"log/slog"
	"time"

	"github.com/dmitrymomot/go-router"
)

type LoggerConfig struct {
	Skip             func(c router.Context) bool
	Logger           *slog.Logger
	Attrs            func(c router.Context, err error) []slog.Attr
	Level            slog.Leveler
	ClientErrorLevel slog.Leveler
	ServerErrorLevel slog.Leveler
	Message          string
	DisableUserAgent bool
}

func Logger[C router.Context](next router.HandlerFunc[C]) router.HandlerFunc[C] {
	return LoggerWithConfig[C](LoggerConfig{})(next)
}

func LoggerWithConfig[C router.Context](cfg LoggerConfig) router.Middleware[C] {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Level == nil {
		cfg.Level = slog.LevelInfo
	}
	if cfg.ClientErrorLevel == nil {
		cfg.ClientErrorLevel = slog.LevelWarn
	}
	if cfg.ServerErrorLevel == nil {
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

			start := time.Now()
			err := next(c)
			req := c.Request()
			status := router.ResolveStatus(c.Response(), err)

			level := cfg.Level
			switch {
			case status >= 500:
				level = cfg.ServerErrorLevel
			case status >= 400:
				level = cfg.ClientErrorLevel
			}

			attrs := make([]slog.Attr, 0, 14)
			attrs = append(attrs,
				slog.String("method", req.Method),
				slog.String("path", req.URL.Path),
				slog.String("route", c.RoutePattern()),
				slog.Int("status", status),
				slog.Duration("took", time.Since(start)),
				slog.Int64("bytes", c.Response().Size),
				slog.String("ip", ClientIP(c)),
				slog.String("host", c.Host()),
				slog.String("proto", req.Proto),
			)
			if pattern := c.RouteHost(); pattern != "" {
				attrs = append(attrs, slog.String("route_host", pattern))
			}
			if agent := req.UserAgent(); agent != "" && !cfg.DisableUserAgent {
				attrs = append(attrs, slog.String("user_agent", agent))
			}
			if referer := req.Referer(); referer != "" {
				attrs = append(attrs, slog.String("referer", referer))
			}
			if id := RequestIDFrom(c); id != "" {
				attrs = append(attrs, slog.String("request_id", id))
			}
			if err != nil {
				attrs = append(attrs, slog.Any("error", err))
			}
			if cfg.Attrs != nil {
				attrs = append(attrs, cfg.Attrs(c, err)...)
			}
			cfg.Logger.LogAttrs(req.Context(), level.Level(), cfg.Message, attrs...)
			return err
		}
	}
}
