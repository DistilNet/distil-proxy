package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/exec-io/distil-proxy/internal/config"
	"github.com/exec-io/distil-proxy/internal/upgrade"
	"github.com/exec-io/distil-proxy/internal/version"
	"github.com/exec-io/distil-proxy/internal/ws"
)

func testConfig() config.Config {
	return config.Config{
		APIKey:    "dk_test_key",
		Server:    "ws://127.0.0.1:1/ws",
		TimeoutMS: 1000,
		LogLevel:  "info",
	}
}

type stubUpgradeManager struct {
	handleStartup  func() (bool, error)
	checkInterval  time.Duration
	checkAndUpdate func(ctx context.Context) (upgrade.CheckResult, error)
}

func (s stubUpgradeManager) HandleStartup() (bool, error) {
	if s.handleStartup != nil {
		return s.handleStartup()
	}
	return false, nil
}

func (s stubUpgradeManager) CheckInterval() time.Duration {
	if s.checkInterval > 0 {
		return s.checkInterval
	}
	return time.Hour
}

func (s stubUpgradeManager) CheckAndUpgrade(ctx context.Context) (upgrade.CheckResult, error) {
	if s.checkAndUpdate != nil {
		return s.checkAndUpdate(ctx)
	}
	return upgrade.CheckResult{}, nil
}

func TestStartWritesStatusAndPreventsDuplicate(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	cfg := testConfig()
	origExecPath := execPathFunc
	origExecCmd := execCmdFunc
	execPathFunc = func() (string, error) { return "/bin/sh", nil }
	execCmdFunc = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "sleep 5")
	}
	defer func() {
		execPathFunc = origExecPath
		execCmdFunc = origExecCmd
	}()

	if err := Start(paths, cfg); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	pid, err := readPID(paths)
	if err != nil {
		t.Fatalf("read pid: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("expected pid > 0, got %d", pid)
	}

	status, err := readStatus(paths)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !status.Running || status.WSState != "starting" {
		t.Fatalf("unexpected status: %+v", status)
	}

	if err := writePID(paths, os.Getpid()); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	err = Start(paths, cfg)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected already running error, got %v", err)
	}
}

func TestStartForegroundAndRunClientFactoryError(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	cfg := testConfig()

	originalFactory := clientFactory
	clientFactory = func(_ ws.ClientConfig) (wsRunner, error) {
		return nil, errors.New("factory failed")
	}
	defer func() { clientFactory = originalFactory }()

	err := StartForeground(context.Background(), paths, cfg, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "factory failed") {
		t.Fatalf("expected factory error, got %v", err)
	}
}

func TestStartErrorPaths(t *testing.T) {
	t.Run("ensure-state-dirs", func(t *testing.T) {
		paths := config.DefaultPaths(t.TempDir())
		if err := os.WriteFile(paths.RootDir, []byte("not-dir"), 0o600); err != nil {
			t.Fatalf("write root file: %v", err)
		}
		if err := Start(paths, testConfig()); err == nil {
			t.Fatal("expected ensure-state-dirs error")
		}
	})

	t.Run("resolve-executable", func(t *testing.T) {
		paths := config.DefaultPaths(t.TempDir())
		origExecPath := execPathFunc
		execPathFunc = func() (string, error) { return "", errors.New("no executable") }
		defer func() { execPathFunc = origExecPath }()

		err := Start(paths, testConfig())
		if err == nil || !strings.Contains(err.Error(), "resolve executable path") {
			t.Fatalf("expected resolve executable error, got %v", err)
		}
	})

	t.Run("open-log-file", func(t *testing.T) {
		paths := config.DefaultPaths(t.TempDir())
		if err := config.EnsureStateDirs(paths); err != nil {
			t.Fatalf("ensure dirs: %v", err)
		}
		paths.LogFile = paths.LogsDir

		err := Start(paths, testConfig())
		if err == nil || !strings.Contains(err.Error(), "open log file") {
			t.Fatalf("expected open log error, got %v", err)
		}
	})

	t.Run("start-child", func(t *testing.T) {
		paths := config.DefaultPaths(t.TempDir())
		origExecPath := execPathFunc
		origExecCmd := execCmdFunc
		execPathFunc = func() (string, error) { return "/bin/sh", nil }
		execCmdFunc = func(_ string, _ ...string) *exec.Cmd { return exec.Command("/definitely/missing-bin") }
		defer func() {
			execPathFunc = origExecPath
			execCmdFunc = origExecCmd
		}()

		err := Start(paths, testConfig())
		if err == nil || !strings.Contains(err.Error(), "start daemon child") {
			t.Fatalf("expected start child error, got %v", err)
		}
	})

	t.Run("write-pid", func(t *testing.T) {
		paths := config.DefaultPaths(t.TempDir())
		paths.PIDFile = filepath.Join(paths.RootDir, "missing", "pid")
		origExecPath := execPathFunc
		origExecCmd := execCmdFunc
		execPathFunc = func() (string, error) { return "/bin/sh", nil }
		execCmdFunc = func(_ string, _ ...string) *exec.Cmd {
			return exec.Command("sh", "-c", "exit 0")
		}
		defer func() {
			execPathFunc = origExecPath
			execCmdFunc = origExecCmd
		}()

		err := Start(paths, testConfig())
		if err == nil || !strings.Contains(err.Error(), "write pid file") {
			t.Fatalf("expected write pid error, got %v", err)
		}
	})

	t.Run("write-status", func(t *testing.T) {
		paths := config.DefaultPaths(t.TempDir())
		paths.StatusFile = paths.RootDir
		origExecPath := execPathFunc
		origExecCmd := execCmdFunc
		execPathFunc = func() (string, error) { return "/bin/sh", nil }
		execCmdFunc = func(_ string, _ ...string) *exec.Cmd {
			return exec.Command("sh", "-c", "exit 0")
		}
		defer func() {
			execPathFunc = origExecPath
			execCmdFunc = origExecCmd
		}()

		err := Start(paths, testConfig())
		if err == nil || !strings.Contains(err.Error(), "replace status file") {
			t.Fatalf("expected write status error, got %v", err)
		}
	})
}

func TestStartForegroundDetectsExistingDaemon(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	cfg := testConfig()
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := writePID(paths, os.Getpid()); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	err := StartForeground(context.Background(), paths, cfg, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected already running error, got %v", err)
	}
}

func TestStartForegroundInvalidLogLevel(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	cfg := testConfig()
	cfg.LogLevel = "not-a-level"

	err := StartForeground(context.Background(), paths, cfg, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid log level") {
		t.Fatalf("expected invalid log level error, got %v", err)
	}
}

func TestStartForegroundEnsureStateDirsError(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := os.WriteFile(paths.RootDir, []byte("file"), 0o600); err != nil {
		t.Fatalf("write root file: %v", err)
	}

	err := StartForeground(context.Background(), paths, testConfig(), io.Discard)
	if err == nil {
		t.Fatal("expected ensure-state-dirs error")
	}
}

func TestStopStalePIDRemovesPIDAndMarksStopped(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := writePID(paths, 999999); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	if err := writeStatus(paths, RuntimeStatus{PID: 999999, Running: true, WSState: "connected"}); err != nil {
		t.Fatalf("write status: %v", err)
	}

	err := Stop(paths)
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("expected ErrNotRunning, got %v", err)
	}
	if _, statErr := os.Stat(paths.PIDFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected pid removed, err=%v", statErr)
	}

	status, err := readStatus(paths)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status.Running || status.WSState != "stopped" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestStopRunningProcess(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}

	out, err := exec.Command("sh", "-c", "sleep 30 >/dev/null 2>&1 & printf '%s' \"$!\"").Output()
	if err != nil {
		t.Fatalf("start detached sleep: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		t.Fatalf("parse detached pid %q: %v", strings.TrimSpace(string(out)), err)
	}
	t.Cleanup(func() {
		_ = exec.Command("kill", "-TERM", strconv.Itoa(pid)).Run()
	})

	if err := writePID(paths, pid); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	if err := Stop(paths); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if processRunning(pid) {
		t.Fatalf("expected pid %d to be stopped", pid)
	}
}

func TestRestartWithStalePID(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := writePID(paths, 999999); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	origExecPath := execPathFunc
	origExecCmd := execCmdFunc
	execPathFunc = func() (string, error) { return "/bin/sh", nil }
	execCmdFunc = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "sleep 5")
	}
	defer func() {
		execPathFunc = origExecPath
		execCmdFunc = origExecCmd
	}()

	if err := Restart(paths, testConfig()); err != nil {
		t.Fatalf("restart failed: %v", err)
	}

	status, err := readStatus(paths)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status.WSState != "starting" {
		t.Fatalf("expected starting state, got %s", status.WSState)
	}
}

func TestRestartReturnsStopError(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := os.WriteFile(paths.PIDFile, []byte("bad-pid"), 0o600); err != nil {
		t.Fatalf("write bad pid file: %v", err)
	}

	if err := Restart(paths, testConfig()); err == nil {
		t.Fatal("expected restart to fail when stop fails")
	}
}

func TestStatusErrorCases(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}

	if err := os.WriteFile(paths.StatusFile, []byte("{"), 0o600); err != nil {
		t.Fatalf("write bad status: %v", err)
	}
	if _, err := Status(paths); err == nil {
		t.Fatal("expected decode status error")
	}

	if err := os.WriteFile(paths.StatusFile, []byte(`{"ws_state":"connected"}`), 0o600); err != nil {
		t.Fatalf("write status: %v", err)
	}
	if err := os.WriteFile(paths.PIDFile, []byte("not-int\n"), 0o600); err != nil {
		t.Fatalf("write bad pid: %v", err)
	}
	if _, err := Status(paths); err == nil {
		t.Fatal("expected parse pid error")
	}
}

func TestHelperFunctionsAndStatusFiles(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())

	if processRunning(-1) {
		t.Fatal("expected invalid pid to be non-running")
	}
	if processRunning(999999) {
		t.Fatal("unexpected running state for invalid pid")
	}

	if err := writePID(paths, os.Getpid()); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	pid, err := readPID(paths)
	if err != nil {
		t.Fatalf("read pid: %v", err)
	}
	if pid != os.Getpid() {
		t.Fatalf("expected pid %d, got %d", os.Getpid(), pid)
	}

	if err := removePIDIfMatches(paths, pid+1); err != nil {
		t.Fatalf("remove mismatched pid: %v", err)
	}
	if _, err := os.Stat(paths.PIDFile); err != nil {
		t.Fatalf("pid should still exist: %v", err)
	}
	if err := removePIDIfMatches(paths, pid); err != nil {
		t.Fatalf("remove matching pid: %v", err)
	}
	if _, err := os.Stat(paths.PIDFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected pid removed, err=%v", err)
	}
	if err := removePID(paths); err != nil {
		t.Fatalf("remove pid should ignore not-exist: %v", err)
	}

	status := RuntimeStatus{
		PID:       1234,
		Running:   true,
		WSState:   "connected",
		UpdatedAt: time.Now().UTC(),
	}
	if err := writeStatus(paths, status); err != nil {
		t.Fatalf("write status: %v", err)
	}
	got, err := readStatus(paths)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if got.PID != 1234 || got.WSState != "connected" {
		t.Fatalf("unexpected status: %+v", got)
	}

	if err := os.WriteFile(paths.StatusFile, []byte("{"), 0o600); err != nil {
		t.Fatalf("write invalid status: %v", err)
	}
	if _, err := readStatus(paths); err == nil {
		t.Fatal("expected decode status error")
	}

	if err := os.WriteFile(paths.PIDFile, []byte("bad"), 0o600); err != nil {
		t.Fatalf("write invalid pid: %v", err)
	}
	if _, err := readPID(paths); err == nil {
		t.Fatal("expected parse pid error")
	}
}

func TestRunningPIDAndMarkStopped(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}

	if err := os.WriteFile(paths.PIDFile, []byte("999999\n"), 0o600); err != nil {
		t.Fatalf("write stale pid: %v", err)
	}
	if pid, ok := runningPID(paths); ok || pid != 0 {
		t.Fatalf("expected no running pid, got pid=%d ok=%t", pid, ok)
	}
	if _, err := os.Stat(paths.PIDFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected stale pid removed, err=%v", err)
	}

	markStopped(paths, 777, "stopped")
	status, err := readStatus(paths)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status.PID != 777 || status.Running || status.WSState != "stopped" {
		t.Fatalf("unexpected status after markStopped: %+v", status)
	}
}

func TestReadLogTailBranches(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}

	lines, err := ReadLogTail(paths, 0)
	if err != nil {
		t.Fatalf("read tail n=0: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("expected empty lines, got %#v", lines)
	}

	if err := os.WriteFile(paths.LogFile, []byte("\n"), 0o600); err != nil {
		t.Fatalf("write blank log: %v", err)
	}
	lines, err = ReadLogTail(paths, 10)
	if err != nil {
		t.Fatalf("read blank tail: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("expected no lines, got %#v", lines)
	}
}

func TestWriteStatusAndPIDErrorPaths(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}

	paths.StatusFile = filepath.Join(paths.RootDir, "missing", "status.json")
	err := writeStatus(paths, RuntimeStatus{WSState: "connected", UpdatedAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("expected write status error")
	}

	paths = config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	paths.PIDFile = filepath.Join(paths.RootDir, "missing", "pid")
	if err := writePID(paths, 1); err == nil {
		t.Fatal("expected write pid error")
	}

	paths = config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	paths.StatusFile = paths.RootDir
	err = writeStatus(paths, RuntimeStatus{WSState: "connected", UpdatedAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("expected replace status error")
	}
}

func TestRemovePIDAndRemovePIDIfMatchesBranches(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}

	paths.PIDFile = paths.RootDir
	if err := removePID(paths); err == nil {
		t.Fatal("expected remove pid error for directory path")
	}

	paths = config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := removePIDIfMatches(paths, 1); err != nil {
		t.Fatalf("expected no-op on missing pid, got %v", err)
	}
	if err := os.WriteFile(paths.PIDFile, []byte("bad"), 0o600); err != nil {
		t.Fatalf("write bad pid: %v", err)
	}
	if err := removePIDIfMatches(paths, 1); err == nil {
		t.Fatal("expected parse error from removePIDIfMatches")
	}
}

func TestReadLogTailMissingFile(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if _, err := ReadLogTail(paths, 10); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected not-exist error, got %v", err)
	}
}

func TestRunErrorPathsAndLastErrorStatus(t *testing.T) {
	t.Run("ensure-state-dirs", func(t *testing.T) {
		paths := config.DefaultPaths(t.TempDir())
		if err := os.WriteFile(paths.RootDir, []byte("file"), 0o600); err != nil {
			t.Fatalf("write root file: %v", err)
		}
		err := Run(context.Background(), paths, testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err == nil {
			t.Fatal("expected ensure-state-dirs error")
		}
	})

	t.Run("write-pid", func(t *testing.T) {
		paths := config.DefaultPaths(t.TempDir())
		paths.PIDFile = filepath.Join(paths.RootDir, "missing", "pid")
		err := Run(context.Background(), paths, testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err == nil {
			t.Fatal("expected write-pid error")
		}
	})

	t.Run("write-status", func(t *testing.T) {
		paths := config.DefaultPaths(t.TempDir())
		paths.StatusFile = paths.RootDir
		err := Run(context.Background(), paths, testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err == nil {
			t.Fatal("expected write-status error")
		}
	})

	t.Run("runtime-client-error-recorded", func(t *testing.T) {
		paths := config.DefaultPaths(t.TempDir())
		cfg := testConfig()

		originalFactory := clientFactory
		clientFactory = func(_ ws.ClientConfig) (wsRunner, error) {
			return fakeRunner{run: func(context.Context) error {
				return errors.New("ws loop failed")
			}}, nil
		}
		defer func() { clientFactory = originalFactory }()

		err := Run(context.Background(), paths, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err == nil || !strings.Contains(err.Error(), "ws loop failed") {
			t.Fatalf("expected run error, got %v", err)
		}

		status, readErr := readStatus(paths)
		if readErr != nil {
			t.Fatalf("read status: %v", readErr)
		}
		if !strings.Contains(status.LastError, "ws loop failed") {
			t.Fatalf("expected last error recorded, got %+v", status)
		}
	})
}

func TestRunHooksAndTickerStatusUpdates(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	cfg := testConfig()

	origFactory := clientFactory
	origTick := statusTick
	statusTick = 5 * time.Millisecond
	clientFactory = func(c ws.ClientConfig) (wsRunner, error) {
		return fakeRunner{run: func(context.Context) error {
			c.Hooks.OnStateChange("connected")
			c.Hooks.OnStateChange("reconnecting")
			c.Hooks.OnHeartbeat(time.Unix(1, 0).UTC())
			c.Hooks.OnJobResult(true, 25)
			c.Hooks.OnJobResult(false, 50)
			c.Hooks.OnError(errors.New("hook failure"))
			time.Sleep(20 * time.Millisecond)
			return nil
		}}, nil
	}
	defer func() {
		clientFactory = origFactory
		statusTick = origTick
	}()

	if err := Run(context.Background(), paths, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	status, err := readStatus(paths)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status.Running {
		t.Fatalf("expected stopped status, got %+v", status)
	}
	if status.ConnectAttempts != 1 || status.Reconnects != 1 {
		t.Fatalf("expected connect/reconnect counts, got %+v", status)
	}
	if status.JobsSuccess != 1 || status.JobsError != 1 || status.JobsServed != 1 {
		t.Fatalf("expected job metrics, got %+v", status)
	}
	if status.AvgLatencyMS <= 0 {
		t.Fatalf("expected avg latency > 0, got %+v", status)
	}
	if status.LastHeartbeat.IsZero() {
		t.Fatalf("expected heartbeat timestamp, got %+v", status)
	}
	if !strings.Contains(status.LastError, "hook failure") {
		t.Fatalf("expected hook error in status, got %+v", status)
	}
}

func TestStopTimeout(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}

	out, err := exec.Command("sh", "-c", "trap '' TERM; sleep 30 >/dev/null 2>&1 & printf '%s' \"$!\"").Output()
	if err != nil {
		t.Fatalf("start ignore-term process: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		t.Fatalf("parse pid %q: %v", strings.TrimSpace(string(out)), err)
	}
	t.Cleanup(func() {
		_ = exec.Command("kill", "-KILL", strconv.Itoa(pid)).Run()
	})

	if err := writePID(paths, pid); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	origTimeout := stopTimeout
	origPoll := stopPoll
	stopTimeout = 200 * time.Millisecond
	stopPoll = 20 * time.Millisecond
	defer func() {
		stopTimeout = origTimeout
		stopPoll = origPoll
	}()

	err = Stop(paths)
	if err == nil || !strings.Contains(err.Error(), "timed out waiting") {
		t.Fatalf("expected timeout stop error, got %v", err)
	}
}

func TestStopSignalErrorWithInjectedKill(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := writePID(paths, os.Getpid()); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	origKill := killFunc
	killFunc = func(_ int, sig syscall.Signal) error {
		if sig == 0 {
			return nil
		}
		return errors.New("deny signal")
	}
	defer func() { killFunc = origKill }()

	err := Stop(paths)
	if err == nil || !strings.Contains(err.Error(), "signal daemon pid") {
		t.Fatalf("expected signal error, got %v", err)
	}
}

func TestStatusDefaultsWhenNoPIDAndEmptyStatus(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := os.WriteFile(paths.StatusFile, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write status file: %v", err)
	}

	status, err := Status(paths)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Running || status.WSState != "stopped" {
		t.Fatalf("unexpected status defaults: %+v", status)
	}
	if status.UpdatedAt.IsZero() {
		t.Fatalf("expected updated_at to be set: %+v", status)
	}
}

func TestNewWSClientAndWriterErrorBranches(t *testing.T) {
	if _, err := newWSClient(ws.ClientConfig{ServerURL: "wss://proxy.distil.net/ws", APIKey: "dk_test"}); err != nil {
		t.Fatalf("new ws client: %v", err)
	}

	paths := config.DefaultPaths(t.TempDir())
	if err := os.WriteFile(paths.RootDir, []byte("file"), 0o600); err != nil {
		t.Fatalf("write root file: %v", err)
	}
	if err := writePID(paths, 1); err == nil {
		t.Fatal("expected ensure-state-dirs error for writePID")
	}

	paths = config.DefaultPaths(t.TempDir())
	origMarshal := marshalStatus
	marshalStatus = func(any, string, string) ([]byte, error) {
		return nil, errors.New("encode failed")
	}
	defer func() { marshalStatus = origMarshal }()

	err := writeStatus(paths, RuntimeStatus{WSState: "connected"})
	if err == nil || !strings.Contains(err.Error(), "encode status") {
		t.Fatalf("expected encode status error, got %v", err)
	}
}

func TestReadLogTailReadError(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := os.WriteFile(paths.LogFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	origReadAll := readAllFunc
	readAllFunc = func(io.Reader) ([]byte, error) {
		return nil, errors.New("read failed")
	}
	defer func() { readAllFunc = origReadAll }()

	_, err := ReadLogTail(paths, 10)
	if err == nil || !strings.Contains(err.Error(), "read log file") {
		t.Fatalf("expected read log error, got %v", err)
	}
}

func TestNewUpgradeManagerBranches(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	cfg := testConfig()

	if mgr := newUpgradeManager(paths, cfg); mgr != nil {
		t.Fatal("expected nil manager when auto-upgrade is disabled")
	}

	origExecPath := execPathFunc
	defer func() { execPathFunc = origExecPath }()

	execPathFunc = func() (string, error) { return "", errors.New("missing executable") }
	cfg.AutoUpgrade = true
	if mgr := newUpgradeManager(paths, cfg); mgr != nil {
		t.Fatal("expected nil manager when executable path lookup fails")
	}

	execPath := filepath.Join(paths.RootDir, "distil-proxy")
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := os.WriteFile(execPath, []byte("bin"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	execPathFunc = func() (string, error) { return execPath, nil }

	cfg.UpgradeCheckHours = 0
	mgr := newUpgradeManager(paths, cfg)
	if mgr == nil {
		t.Fatal("expected manager when auto-upgrade is enabled")
	}
	if got := mgr.CheckInterval(); got != time.Duration(config.DefaultUpgradeCheckHours)*time.Hour {
		t.Fatalf("expected default check interval, got %v", got)
	}

	cfg.UpgradeCheckHours = 2
	mgr = newUpgradeManager(paths, cfg)
	if mgr == nil {
		t.Fatal("expected manager for explicit interval")
	}
	if got := mgr.CheckInterval(); got != 2*time.Hour {
		t.Fatalf("expected 2h interval, got %v", got)
	}
}

func TestRestartProcessBranches(t *testing.T) {
	t.Run("exec-path-error", func(t *testing.T) {
		origExecPath := execPathFunc
		execPathFunc = func() (string, error) { return "", errors.New("no executable") }
		defer func() { execPathFunc = origExecPath }()

		if err := restartProcess(); err == nil || !strings.Contains(err.Error(), "resolve executable path") {
			t.Fatalf("expected executable path error, got %v", err)
		}
	})

	t.Run("start-error", func(t *testing.T) {
		origExecPath := execPathFunc
		origExecCmd := execCmdFunc
		execPathFunc = func() (string, error) { return "/bin/sh", nil }
		execCmdFunc = func(_ string, _ ...string) *exec.Cmd { return exec.Command("/definitely/missing-binary") }
		defer func() {
			execPathFunc = origExecPath
			execCmdFunc = origExecCmd
		}()

		if err := restartProcess(); err == nil || !strings.Contains(err.Error(), "restart process") {
			t.Fatalf("expected restart start error, got %v", err)
		}
	})

	t.Run("success-calls-exit", func(t *testing.T) {
		origExecPath := execPathFunc
		origExecCmd := execCmdFunc
		origExit := exitFunc
		execPathFunc = func() (string, error) { return "/bin/sh", nil }
		execCmdFunc = func(_ string, _ ...string) *exec.Cmd { return exec.Command("sh", "-c", "true") }
		var exitCode int
		exitCalls := 0
		exitFunc = func(code int) {
			exitCode = code
			exitCalls++
		}
		defer func() {
			execPathFunc = origExecPath
			execCmdFunc = origExecCmd
			exitFunc = origExit
		}()

		if err := restartProcess(); err != nil {
			t.Fatalf("expected restart success, got %v", err)
		}
		if exitCalls != 1 || exitCode != 0 {
			t.Fatalf("expected exit(0) once, calls=%d code=%d", exitCalls, exitCode)
		}
	})
}

func TestRunAutoUpgradeStartupPaths(t *testing.T) {
	t.Run("startup-state-error-recorded", func(t *testing.T) {
		paths := config.DefaultPaths(t.TempDir())
		if err := config.EnsureStateDirs(paths); err != nil {
			t.Fatalf("ensure dirs: %v", err)
		}
		if err := os.WriteFile(paths.UpgradeStateFile, []byte("{bad-json"), 0o600); err != nil {
			t.Fatalf("write invalid upgrade state: %v", err)
		}

		execPath := filepath.Join(paths.RootDir, "distil-proxy")
		if err := os.WriteFile(execPath, []byte("bin"), 0o755); err != nil {
			t.Fatalf("write executable: %v", err)
		}

		origFactory := clientFactory
		origExecPath := execPathFunc
		clientFactory = func(_ ws.ClientConfig) (wsRunner, error) {
			return fakeRunner{run: func(context.Context) error { return nil }}, nil
		}
		execPathFunc = func() (string, error) { return execPath, nil }
		defer func() {
			clientFactory = origFactory
			execPathFunc = origExecPath
		}()

		cfg := testConfig()
		cfg.AutoUpgrade = true
		cfg.UpgradeCheckHours = 1

		if err := Run(context.Background(), paths, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
			t.Fatalf("run failed: %v", err)
		}

		status, err := readStatus(paths)
		if err != nil {
			t.Fatalf("read status: %v", err)
		}
		if !strings.Contains(status.LastError, "auto-upgrade startup check failed") {
			t.Fatalf("expected startup check error in status, got %+v", status)
		}
	})

	t.Run("rollback-restarts-process", func(t *testing.T) {
		paths := config.DefaultPaths(t.TempDir())
		if err := config.EnsureStateDirs(paths); err != nil {
			t.Fatalf("ensure dirs: %v", err)
		}

		execPath := filepath.Join(paths.RootDir, "distil-proxy")
		if err := os.WriteFile(execPath, []byte("new"), 0o755); err != nil {
			t.Fatalf("write new binary: %v", err)
		}
		if err := os.WriteFile(execPath+".bak", []byte("old"), 0o755); err != nil {
			t.Fatalf("write backup binary: %v", err)
		}

		state := upgrade.UpgradeState{
			UpgradedAt:  time.Now().UTC(),
			FromVersion: "0.0.1",
			ToVersion:   version.DefaultInfo().Version,
			StartedOnce: true,
			StartedAt:   time.Now().UTC(),
		}
		payload, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("marshal state: %v", err)
		}
		if err := os.WriteFile(paths.UpgradeStateFile, payload, 0o600); err != nil {
			t.Fatalf("write upgrade state: %v", err)
		}

		origFactory := clientFactory
		origExecPath := execPathFunc
		origExecCmd := execCmdFunc
		origExit := exitFunc
		clientCalled := false
		exitCalled := false
		clientFactory = func(_ ws.ClientConfig) (wsRunner, error) {
			clientCalled = true
			return fakeRunner{run: func(context.Context) error { return nil }}, nil
		}
		execPathFunc = func() (string, error) { return execPath, nil }
		execCmdFunc = func(_ string, _ ...string) *exec.Cmd { return exec.Command("sh", "-c", "true") }
		exitFunc = func(int) { exitCalled = true }
		defer func() {
			clientFactory = origFactory
			execPathFunc = origExecPath
			execCmdFunc = origExecCmd
			exitFunc = origExit
		}()

		cfg := testConfig()
		cfg.AutoUpgrade = true
		cfg.UpgradeCheckHours = 1

		if err := Run(context.Background(), paths, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
			t.Fatalf("run failed: %v", err)
		}
		if clientCalled {
			t.Fatal("expected websocket client not to be created during rollback restart path")
		}
		if !exitCalled {
			t.Fatal("expected restart path to invoke exit function")
		}

		installed, err := os.ReadFile(execPath)
		if err != nil {
			t.Fatalf("read rolled-back binary: %v", err)
		}
		if string(installed) != "old" {
			t.Fatalf("expected rolled-back binary content old, got %q", string(installed))
		}
		if _, err := os.Stat(paths.UpgradeStateFile); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected upgrade state file removed, err=%v", err)
		}
	})
}

func TestRunAutoUpgradeTickerBranchesAndRestart(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	execPath := filepath.Join(paths.RootDir, "distil-proxy")
	if err := os.WriteFile(execPath, []byte("bin"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}

	origFactory := clientFactory
	origUpgraderFactory := upgradeManagerFactory
	origExecPath := execPathFunc
	origExecCmd := execCmdFunc
	origExit := exitFunc
	defer func() {
		clientFactory = origFactory
		upgradeManagerFactory = origUpgraderFactory
		execPathFunc = origExecPath
		execCmdFunc = origExecCmd
		exitFunc = origExit
	}()

	clientFactory = func(_ ws.ClientConfig) (wsRunner, error) {
		return fakeRunner{run: func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		}}, nil
	}

	var checks atomic.Int32
	upgradeManagerFactory = func(config.Paths, config.Config) upgradeManager {
		return stubUpgradeManager{
			checkInterval: 5 * time.Millisecond,
			checkAndUpdate: func(context.Context) (upgrade.CheckResult, error) {
				if checks.Add(1) == 1 {
					return upgrade.CheckResult{}, errors.New("check failed")
				}
				return upgrade.CheckResult{Applied: true}, nil
			},
		}
	}
	execPathFunc = func() (string, error) { return execPath, nil }
	execCmdFunc = func(_ string, _ ...string) *exec.Cmd { return exec.Command("sh", "-c", "true") }
	exitCalled := false
	exitFunc = func(int) { exitCalled = true }

	cfg := testConfig()
	cfg.AutoUpgrade = true
	cfg.UpgradeCheckHours = 1

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Run(ctx, paths, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !exitCalled {
		t.Fatal("expected restart path to invoke exit")
	}
	if checks.Load() < 2 {
		t.Fatalf("expected multiple upgrade checks, got %d", checks.Load())
	}

	status, err := readStatus(paths)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !strings.Contains(status.LastError, "auto-upgrade check failed") {
		t.Fatalf("expected upgrade check failure to be recorded, got %+v", status)
	}
	if status.WSState != "restarting" {
		t.Fatalf("expected restarting state before restart, got %+v", status)
	}
}

func TestRunFinalizationErrorPaths(t *testing.T) {
	t.Run("final-write-status-error", func(t *testing.T) {
		paths := config.DefaultPaths(t.TempDir())
		cfg := testConfig()

		origFactory := clientFactory
		origMarshal := marshalStatus
		defer func() {
			clientFactory = origFactory
			marshalStatus = origMarshal
		}()

		clientFactory = func(_ ws.ClientConfig) (wsRunner, error) {
			return fakeRunner{run: func(context.Context) error { return nil }}, nil
		}

		var marshalCalls atomic.Int32
		marshalStatus = func(v any, prefix, indent string) ([]byte, error) {
			if marshalCalls.Add(1) >= 2 {
				return nil, errors.New("encode failed late")
			}
			return json.MarshalIndent(v, prefix, indent)
		}

		err := Run(context.Background(), paths, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err == nil || !strings.Contains(err.Error(), "encode status") {
			t.Fatalf("expected final write status error, got %v", err)
		}
	})

	t.Run("remove-pid-if-matches-error", func(t *testing.T) {
		paths := config.DefaultPaths(t.TempDir())
		cfg := testConfig()

		origFactory := clientFactory
		defer func() { clientFactory = origFactory }()

		clientFactory = func(_ ws.ClientConfig) (wsRunner, error) {
			return fakeRunner{run: func(context.Context) error {
				if err := os.WriteFile(paths.PIDFile, []byte("bad-pid"), 0o600); err != nil {
					t.Fatalf("write bad pid: %v", err)
				}
				return nil
			}}, nil
		}

		err := Run(context.Background(), paths, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err == nil || !strings.Contains(err.Error(), "parse pid file") {
			t.Fatalf("expected removePIDIfMatches parse error, got %v", err)
		}
	})
}

func TestStatusAndReadLogTailAdditionalCoverage(t *testing.T) {
	t.Run("status-computes-uptime-from-started-at", func(t *testing.T) {
		paths := config.DefaultPaths(t.TempDir())
		if err := config.EnsureStateDirs(paths); err != nil {
			t.Fatalf("ensure dirs: %v", err)
		}
		status := RuntimeStatus{
			WSState:    "connected",
			StartedAt:  time.Now().UTC().Add(-2 * time.Second),
			UpdatedAt:  time.Now().UTC(),
			LastError:  "",
			PID:        0,
			JobsServed: 1,
		}
		if err := writeStatus(paths, status); err != nil {
			t.Fatalf("write status: %v", err)
		}

		got, err := Status(paths)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if got.UptimeSeconds <= 0 {
			t.Fatalf("expected uptime to be computed, got %+v", got)
		}
	})

	t.Run("read-log-tail-returns-all-lines-when-under-limit", func(t *testing.T) {
		paths := config.DefaultPaths(t.TempDir())
		if err := config.EnsureStateDirs(paths); err != nil {
			t.Fatalf("ensure dirs: %v", err)
		}
		if err := os.WriteFile(paths.LogFile, []byte("a\nb\n"), 0o600); err != nil {
			t.Fatalf("write log file: %v", err)
		}
		lines, err := ReadLogTail(paths, 10)
		if err != nil {
			t.Fatalf("read log tail: %v", err)
		}
		if len(lines) != 2 || lines[0] != "a" || lines[1] != "b" {
			t.Fatalf("unexpected log lines: %v", lines)
		}
	})
}
