package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/distilnet/distil-proxy/internal/config"
	"github.com/distilnet/distil-proxy/internal/daemon"
	"github.com/distilnet/distil-proxy/internal/version"
)

func TestStatusOutputGolden(t *testing.T) {
	origStatusFunc := statusDaemonFunc
	origConfigLoad := statusConfigLoadFunc
	origNowFunc := statusNowFunc
	origLocal := time.Local
	t.Cleanup(func() {
		statusDaemonFunc = origStatusFunc
		statusConfigLoadFunc = origConfigLoad
		statusNowFunc = origNowFunc
		time.Local = origLocal
	})

	fixedLoc := time.FixedZone("AEDT", 11*60*60)
	time.Local = fixedLoc
	statusNowFunc = func() time.Time {
		return time.Date(2026, 3, 2, 13, 25, 25, 0, fixedLoc)
	}
	statusDaemonFunc = func(_ config.Paths) (daemon.RuntimeStatus, error) {
		return daemon.RuntimeStatus{
			Running:         true,
			PID:             55895,
			WSState:         "reconnecting",
			StartedAt:       time.Date(2026, 3, 2, 11, 33, 0, 0, fixedLoc),
			UptimeSeconds:   6745,
			JobsServed:      0,
			ConnectAttempts: 109,
			Reconnects:      109,
			JobsSuccess:     0,
			JobsError:       0,
			AvgLatencyMS:    0,
			LatencyLE100MS:  0,
			LatencyLE500MS:  0,
			LatencyLE1000MS: 0,
			LatencyGT1000MS: 0,
			LastError:       "read websocket message: failed to get reader: use of closed network connection",
		}, nil
	}
	statusConfigLoadFunc = func(_ config.Paths) (config.Config, error) {
		return config.Config{
			APIKey:            "dpk_test",
			Email:             "operator@example.com",
			Server:            "ws://proxy.distil.net/ws",
			TimeoutMS:         30000,
			LogLevel:          "info",
			AutoUpgrade:       true,
			UpgradeCheckHours: 6,
		}, nil
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	cmd := NewRootCmd(version.Info{Version: "0.9.2"})
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

func TestStatusJSONOutput(t *testing.T) {
	origStatusFunc := statusDaemonFunc
	origConfigLoad := statusConfigLoadFunc
	origNowFunc := statusNowFunc
	origLocal := time.Local
	t.Cleanup(func() {
		statusDaemonFunc = origStatusFunc
		statusConfigLoadFunc = origConfigLoad
		statusNowFunc = origNowFunc
		time.Local = origLocal
	})

	fixedLoc := time.FixedZone("AEDT", 11*60*60)
	time.Local = fixedLoc
	statusNowFunc = func() time.Time {
		return time.Date(2026, 3, 2, 13, 25, 25, 0, fixedLoc)
	}
	statusDaemonFunc = func(_ config.Paths) (daemon.RuntimeStatus, error) {
		return daemon.RuntimeStatus{
			Running:         true,
			PID:             55895,
			WSState:         "reconnecting",
			StartedAt:       time.Date(2026, 3, 2, 11, 33, 0, 0, fixedLoc),
			UptimeSeconds:   6745,
			JobsServed:      0,
			ConnectAttempts: 109,
			Reconnects:      109,
			JobsSuccess:     0,
			JobsError:       0,
			AvgLatencyMS:    0,
			LatencyLE100MS:  0,
			LatencyLE500MS:  0,
			LatencyLE1000MS: 0,
			LatencyGT1000MS: 0,
			LastError:       "read websocket message: failed to get reader: use of closed network connection",
		}, nil
	}
	statusConfigLoadFunc = func(_ config.Paths) (config.Config, error) {
		return config.Config{
			APIKey:            "dpk_test",
			Email:             "operator@example.com",
			Server:            "ws://proxy.distil.net/ws",
			TimeoutMS:         30000,
			LogLevel:          "info",
			AutoUpgrade:       true,
			UpgradeCheckHours: 6,
		}, nil
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	cmd := NewRootCmd(version.Info{Version: "0.9.2"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"status", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute status --json command: %v", err)
	}

	var got statusOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode status json: %v", err)
	}

	if got.Version != "0.9.2" {
		t.Fatalf("unexpected version: %q", got.Version)
	}
	if got.Status != "reconnecting" {
		t.Fatalf("unexpected status: %q", got.Status)
	}
	if got.PID != 55895 {
		t.Fatalf("unexpected pid: %d", got.PID)
	}
	if got.UptimeHuman != "1h 52m 25s" {
		t.Fatalf("unexpected uptime_human: %q", got.UptimeHuman)
	}
	if got.Email != "operator@example.com" {
		t.Fatalf("unexpected email: %q", got.Email)
	}
	if got.WebSocket.URL != "ws://proxy.distil.net/ws" {
		t.Fatalf("unexpected websocket url: %q", got.WebSocket.URL)
	}
	if got.WebSocket.LastError == "" {
		t.Fatal("expected websocket last_error")
	}
	if got.Location.City != "Sydney" || got.Location.Country != "AU" || got.Location.Type != "residential" {
		t.Fatalf("unexpected location: %+v", got.Location)
	}
}
