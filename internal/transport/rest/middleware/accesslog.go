package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

// AccessLog writes one structured log line per request.
// The level follows the status: 5xx = error, 4xx = warn, otherwise info.
func AccessLog(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		status := c.Writer.Status()
		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}

		path := c.FullPath() // route pattern, not the raw URL: safe from high cardinality and sensitive data
		if path == "" {
			path = "unmatched"
		}
		attrs := []slog.Attr{
			slog.String("request_id", RequestIDFrom(c)),
			slog.String("method", c.Request.Method),
			slog.String("route", path),
			slog.Int("status", status),
			slog.Duration("latency", time.Since(start)),
			slog.String("client_ip", c.ClientIP()),
		}
		// The text of an unexpected error goes to the log only (never to the client), and only for 5xx.
		if status >= 500 && len(c.Errors) > 0 {
			attrs = append(attrs, slog.String("error", c.Errors.String()))
		}
		log.LogAttrs(c.Request.Context(), level, "http_request", attrs...)
	}
}
