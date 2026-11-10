// Package logx builds the process logger from the settings.
package logx

import (
	"log/slog"
	"os"
)

// New returns the logger every layer writes to. format is "text" (default) or
// "json"; level is debug, info (default), warn or error.
func New(format, level string) *slog.Logger {
	lvl := slog.LevelInfo
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
