package middleware

import (
	"log/slog"
	"time"

	"github.com/dmitrymomot/go-router"
)

// LoggerConfig configures [LoggerWithConfig].
//
// The three level fields take a [slog.Leveler], and nil is what reads as
// unset. A [slog.Level] is one, so slog.LevelWarn goes in as it is, and
// [slog.LevelInfo] selects Info. A field of type slog.Level could not tell
// that choice from the zero value it starts at, which left Info the one level
// of the four that the two error fields could not name. A [slog.LevelVar] in
// any of the three moves that level while the server runs.
type LoggerConfig struct {
	// Skip passes a request straight to the next handler when it returns true.
	// Use it to keep a health check out of the log.
	Skip func(c router.Context) bool

	// Logger receives the records. It defaults to [slog.Default].
	Logger *slog.Logger

	// Attrs appends application attributes to the record. It receives the
	// error that the handler returned, which is nil for a request that
	// succeeded:
	//
	//	Attrs: func(c router.Context, err error) []slog.Attr {
	//		return []slog.Attr{slog.String("tenant", c.Param("tenant"))}
	//	}
	Attrs func(c router.Context, err error) []slog.Attr

	// Level is the level of a record for a status below 400. It defaults to
	// [slog.LevelInfo].
	Level slog.Leveler

	// ClientErrorLevel is the level for a status from 400 to 499. It defaults
	// to [slog.LevelWarn].
	ClientErrorLevel slog.Leveler

	// ServerErrorLevel is the level for a status of 500 and above. It defaults
	// to [slog.LevelError].
	ServerErrorLevel slog.Leveler

	// Message is the text of the record. It defaults to "request".
	Message string

	// DisableUserAgent leaves the user_agent field out. The header is long and
	// repeats on every line of a session with one client, so a service that
	// reads its agents elsewhere turns it off. The field is on by default,
	// because a request log that cannot tell a browser from a bot answers
	// half the questions that a log is read for.
	DisableUserAgent bool
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
//
// It describes the request as the chain left it, not as it arrived, so a
// method override below this middleware reports the method that answered and a
// rewrite reports the path that matched.
//
// The record holds the method, the path, the route, the status, the duration,
// the size, the client address, the host, the host pattern of the route, the
// protocol, the user agent and the referer. A field whose header the request
// did not carry stays out of the record, and so does the host pattern of a
// route that answers every host.
//
// The host and the route host are the pair that a multi-tenant router is read
// through: [router.Base.Host] is the host that the client named, and
// [router.Base.RouteHost] is the pattern that answered it, so a line reports
// which tenant the request reached and which host scope served it.
//
// Use Attrs for the fields of the application. This middleware does not carry
// a switch per field, because the library logs through log/slog and a handler
// there drops or renames a field on its own.
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
			// The request comes off the context after the chain ran, so that a
			// middleware below this one that replaced it, a method override or
			// a rewrite among them, is what the record describes.
			req := c.Request()
			status := statusOf(c, err)

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
				// The path, and never the request URI: a query string carries
				// a token, a reset code or a search often enough that logging
				// one leaks it to everyone who reads the log.
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
			// The level is read here rather than at the call above, so a
			// [slog.LevelVar] answers with the level it holds now.
			cfg.Logger.LogAttrs(req.Context(), level.Level(), cfg.Message, attrs...)
			return err
		}
	}
}
