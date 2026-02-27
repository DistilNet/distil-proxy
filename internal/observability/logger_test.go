package observability

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input   string
		expects slog.Level
		wantErr bool
	}{
		{input: "debug", expects: slog.LevelDebug},
		{input: "info", expects: slog.LevelInfo},
		{input: "", expects: slog.LevelInfo},
		{input: "warning", expects: slog.LevelWarn},
		{input: "error", expects: slog.LevelError},
		{input: "bad", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseLevel(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.expects {
				t.Fatalf("expected %v, got %v", tc.expects, got)
			}
		})
	}
}

func TestEventFieldsAttrs(t *testing.T) {
	attrs := EventFields{JobID: "job_123", Event: "fetch_complete", DurationMS: 245}.Attrs()
	if len(attrs) != 3 {
		t.Fatalf("expected 3 attrs, got %d", len(attrs))
	}
}

func TestLogIncludesCorrelationFields(t *testing.T) {
	buf := &bytes.Buffer{}
	logger, err := NewLogger("info", buf)
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	Log(context.Background(), logger, slog.LevelInfo, "fetch finished", EventFields{
		JobID:      "job_abc",
		Event:      "fetch_complete",
		DurationMS: 123,
	})

	out := buf.String()
	for _, pattern := range []string{"\"job_id\":\"job_abc\"", "\"event\":\"fetch_complete\"", "\"duration_ms\":123"} {
		if !strings.Contains(out, pattern) {
			t.Fatalf("expected %q in output: %s", pattern, out)
		}
	}
}
