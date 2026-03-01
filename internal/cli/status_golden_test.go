package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/exec-io/distil-proxy/internal/config"
	"github.com/exec-io/distil-proxy/internal/version"
)

func TestStatusOutputGolden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	paths := config.DefaultPaths(home)
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}

	statusJSON := `{
  "pid": 0,
  "running": true,
  "ws_state": "connected",
  "updated_at": "2026-01-01T00:00:00Z",
  "jobs_served": 7,
  "uptime_seconds": 42,
  "connect_attempts": 2,
  "reconnects": 1,
  "jobs_success": 7,
  "jobs_error": 1,
  "avg_latency_ms": 123,
  "latency_le_100_ms": 2,
  "latency_le_500_ms": 4,
  "latency_le_1000_ms": 1,
  "latency_gt_1000_ms": 0
}
`

	if err := os.WriteFile(paths.StatusFile, []byte(statusJSON), 0o600); err != nil {
		t.Fatalf("write status file: %v", err)
	}

	cmd := NewRootCmd(version.DefaultInfo())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"status"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute status command: %v", err)
	}

	goldenPath := filepath.Join("testdata", "status_output.golden")
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}

	if out.String() != string(want) {
		t.Fatalf("status output mismatch\n--- got ---\n%s\n--- want ---\n%s", out.String(), string(want))
	}
}
