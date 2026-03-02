package observability

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// EventFields defines standard correlation fields for runtime logs.
type EventFields struct {
	JobID      string
	Event      string
	DurationMS int64
}

// Attrs returns structured attributes for consistent logging schema.
func (f EventFields) Attrs() []slog.Attr {
	attrs := make([]slog.Attr, 0, 3)
	if strings.TrimSpace(f.JobID) != "" {
		attrs = append(attrs, slog.String("job_id", f.JobID))
	}
	if strings.TrimSpace(f.Event) != "" {
		attrs = append(attrs, slog.String("event", f.Event))
	}
	if f.DurationMS >= 0 {
		attrs = append(attrs, slog.Int64("duration_ms", f.DurationMS))
	}

	return attrs
}

// NewLogger builds a JSON structured logger at the requested level.
func NewLogger(level string, w io.Writer) (*slog.Logger, error) {
	parsedLevel, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}

	if w == nil {
		w = os.Stdout
	}

	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: parsedLevel})
	return slog.New(handler), nil
}

// ParseLevel resolves a textual level to slog.Level.
func ParseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q", level)
	}
}

// Log emits a message with standard event fields and optional extra attrs.
func Log(ctx context.Context, logger *slog.Logger, level slog.Level, msg string, fields EventFields, attrs ...slog.Attr) {
	if logger == nil {
		return
	}

	allAttrs := append(fields.Attrs(), attrs...)
	logger.LogAttrs(ctx, level, msg, allAttrs...)
}
