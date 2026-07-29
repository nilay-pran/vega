// Package logger builds the project's structured logger. It is a thin wrapper
// over log/slog so every process logs the same way; nothing here is clever.
package logger

import (
	"io"
	"log/slog"
	"strings"
)

// New returns a JSON slog.Logger writing to w at the given level.
// JSON is used so logs are machine-parseable for the Logs screen and for
// shipping to central (SOC2) log storage.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// ParseLevel maps a config string to a slog.Level, defaulting to Info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
