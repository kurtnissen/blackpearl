// Package platform owns process-wide logging and telemetry setup.
package platform

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// NewLogger constructs a structured JSON logger at the configured level.
func NewLogger(level string, writer io.Writer) (*slog.Logger, error) {
	var configured slog.Level
	switch strings.ToLower(level) {
	case "trace":
		configured = slog.LevelDebug - 4
	case "debug":
		configured = slog.LevelDebug
	case "info":
		configured = slog.LevelInfo
	case "warn":
		configured = slog.LevelWarn
	case "error":
		configured = slog.LevelError
	default:
		return nil, fmt.Errorf("unsupported log level: %q", level)
	}
	handler := slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: configured})
	return slog.New(handler), nil
}
