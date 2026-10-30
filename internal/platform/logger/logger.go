// Package logger creates the *slog.Logger (log/slog, standard library) used across the application.
package logger

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// ParseLevel converts text (debug|info|warn|error) into a slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("logger: unknown level %q (debug|info|warn|error)", s)
	}
}

// New creates a logger. Production mode uses JSON (easy for Loki/ELK/CloudWatch to ingest),
// other modes use text that is pleasant to read in a terminal.
func New(w io.Writer, level string, production bool) (*slog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: lvl}

	var h slog.Handler
	if production {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h), nil
}
