package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/exec-io/distil-proxy/internal/config"
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

func waitForProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processRunning(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !processRunning(pid)
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

	if err := writePID(paths, pid); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	origProcessName := processNameFn
	processNameFn = func(_ int) (string, error) {
		return "/bin/sh", nil
	}
	defer func() { processNameFn = origProcessName }()

	err = Start(paths, cfg)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected already running error, got %v", err)
	}
}

func TestStartDetachesChildSession(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	cfg := testConfig()

	origExecPath := execPathFunc
	origExecCmd := execCmdFunc
	var startedCmd *exec.Cmd
	execPathFunc = func() (string, error) { return "/bin/sh", nil }
	execCmdFunc = func(_ string, _ ...string) *exec.Cmd {
		startedCmd = exec.Command("sh", "-c", "sleep 30")
		return startedCmd
	}
	defer func() {
		execPathFunc = origExecPath
		execCmdFunc = origExecCmd
	}()

	if err := Start(paths, cfg); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if startedCmd == nil || startedCmd.SysProcAttr == nil || !startedCmd.SysProcAttr.Setsid {
		t.Fatalf("expected daemon child to run in detached session, got %+v", startedCmd)
	}
	if startedCmd.Process != nil {
		terminateProcess(startedCmd.Process)
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

func TestStartCleansUpChildWhenWritePIDFails(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	paths.PIDFile = filepath.Join(paths.RootDir, "missing", "pid")
	var startedCmd *exec.Cmd

	origExecPath := execPathFunc
	origExecCmd := execCmdFunc
	execPathFunc = func() (string, error) { return "/bin/sh", nil }
	execCmdFunc = func(_ string, _ ...string) *exec.Cmd {
		startedCmd = exec.Command("sh", "-c", "sleep 30")
		return startedCmd
	}
	defer func() {
		execPathFunc = origExecPath
		execCmdFunc = origExecCmd
	}()

	err := Start(paths, testConfig())
	if err == nil || !strings.Contains(err.Error(), "write pid file") {
		t.Fatalf("expected write pid error, got %v", err)
	}
	if startedCmd == nil || startedCmd.Process == nil || startedCmd.Process.Pid <= 0 {
		t.Fatalf("expected child process pid after start failure, got %+v", startedCmd)
	}
	pid := startedCmd.Process.Pid
	t.Cleanup(func() {
		_ = exec.Command("kill", "-KILL", strconv.Itoa(pid)).Run()
	})
	if !waitForProcessExit(pid, 2*time.Second) {
		t.Fatalf("expected child pid %d to be terminated after writePID failure", pid)
	}
}

func TestStartCleansUpChildWhenWriteStatusFails(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	paths.StatusFile = paths.RootDir
	var startedCmd *exec.Cmd

	origExecPath := execPathFunc
	origExecCmd := execCmdFunc
	execPathFunc = func() (string, error) { return "/bin/sh", nil }
	execCmdFunc = func(_ string, _ ...string) *exec.Cmd {
		startedCmd = exec.Command("sh", "-c", "sleep 30")
		return startedCmd
	}
	defer func() {
		execPathFunc = origExecPath
		execCmdFunc = origExecCmd
	}()

	err := Start(paths, testConfig())
	if err == nil || !strings.Contains(err.Error(), "replace status file") {
		t.Fatalf("expected write status error, got %v", err)
	}
	if startedCmd == nil || startedCmd.Process == nil || startedCmd.Process.Pid <= 0 {
		t.Fatalf("expected child process pid after start failure, got %+v", startedCmd)
	}
	pid := startedCmd.Process.Pid
	t.Cleanup(func() {
		_ = exec.Command("kill", "-KILL", strconv.Itoa(pid)).Run()
	})
	if !waitForProcessExit(pid, 2*time.Second) {
		t.Fatalf("expected child pid %d to be terminated after writeStatus failure", pid)
	}
	if _, statErr := os.Stat(paths.PIDFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected pid file removed after status write failure, err=%v", statErr)
	}
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
	expectedPath, err := execPathFunc()
	if err != nil {
		t.Fatalf("resolve executable path: %v", err)
	}
	origProcessName := processNameFn
	processNameFn = func(_ int) (string, error) {
		return expectedPath, nil
	}
	defer func() { processNameFn = origProcessName }()

	err = StartForeground(context.Background(), paths, cfg, io.Discard)
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

	expectedPath, err := execPathFunc()
	if err != nil {
		t.Fatalf("resolve executable path: %v", err)
	}
	origProcessName := processNameFn
	processNameFn = func(_ int) (string, error) {
		return expectedPath, nil
	}
	defer func() { processNameFn = origProcessName }()

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

func TestRunningPIDRejectsOwnershipMismatch(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := writePID(paths, os.Getpid()); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	origProcessName := processNameFn
	processNameFn = func(_ int) (string, error) {
		return "not-distil-proxy", nil
	}
	defer func() { processNameFn = origProcessName }()

	if pid, ok := runningPID(paths); ok || pid != 0 {
		t.Fatalf("expected no owned running pid, got pid=%d ok=%t", pid, ok)
	}
	if _, err := os.Stat(paths.PIDFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ownership-mismatched pid removed, err=%v", err)
	}
}

func TestDaemonOwnsPIDBranches(t *testing.T) {
	origExecPath := execPathFunc
	origProcessName := processNameFn
	t.Cleanup(func() {
		execPathFunc = origExecPath
		processNameFn = origProcessName
	})

	if daemonOwnsPID(0) {
		t.Fatal("expected pid<=0 to be rejected")
	}

	execPathFunc = func() (string, error) { return "", errors.New("no executable") }
	if daemonOwnsPID(os.Getpid()) {
		t.Fatal("expected exec path error to reject ownership")
	}

	execPathFunc = func() (string, error) { return "   ", nil }
	if daemonOwnsPID(os.Getpid()) {
		t.Fatal("expected blank executable name to reject ownership")
	}

	execPathFunc = func() (string, error) { return "/tmp/distil-proxy", nil }
	processNameFn = func(_ int) (string, error) { return "", errors.New("process lookup failed") }
	if daemonOwnsPID(os.Getpid()) {
		t.Fatal("expected process lookup error to reject ownership")
	}

	processNameFn = func(_ int) (string, error) { return "/tmp/other-proc", nil }
	if daemonOwnsPID(os.Getpid()) {
		t.Fatal("expected name mismatch to reject ownership")
	}

	processNameFn = func(_ int) (string, error) { return "/tmp/distil-proxy --foreground", nil }
	if !daemonOwnsPID(os.Getpid()) {
		t.Fatal("expected exact executable command match to accept ownership")
	}

	processNameFn = func(_ int) (string, error) { return "/tmp/distil-proxy-other --foreground", nil }
	if daemonOwnsPID(os.Getpid()) {
		t.Fatal("expected executable prefix collisions to be rejected")
	}
}

func TestDaemonOwnsPIDAcceptsSymlinkedExecutablePaths(t *testing.T) {
	tmpDir := t.TempDir()
	realBinary := filepath.Join(tmpDir, "distil-proxy-real")
	if err := os.WriteFile(realBinary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write real executable: %v", err)
	}
	expectedAlias := filepath.Join(tmpDir, "distil-proxy-alias-a")
	processAlias := filepath.Join(tmpDir, "distil-proxy-alias-b")
	if err := os.Symlink(realBinary, expectedAlias); err != nil {
		t.Fatalf("symlink expected alias: %v", err)
	}
	if err := os.Symlink(realBinary, processAlias); err != nil {
		t.Fatalf("symlink process alias: %v", err)
	}

	origExecPath := execPathFunc
	origProcessName := processNameFn
	execPathFunc = func() (string, error) { return expectedAlias, nil }
	processNameFn = func(_ int) (string, error) { return `"` + processAlias + `" __run`, nil }
	defer func() {
		execPathFunc = origExecPath
		processNameFn = origProcessName
	}()

	if !daemonOwnsPID(os.Getpid()) {
		t.Fatal("expected symlink aliases of the same binary to be treated as daemon ownership")
	}
}

func TestCommandExecutablePathParsing(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
		wantOK bool
	}{
		{name: "empty", input: "", want: "", wantOK: false},
		{name: "single-token", input: "/tmp/distil-proxy", want: "/tmp/distil-proxy", wantOK: true},
		{name: "with-args", input: "/tmp/distil-proxy __run", want: "/tmp/distil-proxy", wantOK: true},
		{name: "double-quoted", input: `"/tmp/with spaces/distil-proxy" __run`, want: "/tmp/with spaces/distil-proxy", wantOK: true},
		{name: "double-quoted-escaped", input: `"/tmp/distil-proxy\"quoted\"" __run`, want: `/tmp/distil-proxy"quoted"`, wantOK: true},
		{name: "single-quoted", input: `'/tmp/with spaces/distil-proxy' __run`, want: "/tmp/with spaces/distil-proxy", wantOK: true},
		{name: "unterminated-quote", input: `"/tmp/distil-proxy`, want: "", wantOK: false},
		{name: "empty-quoted", input: `"" __run`, want: "", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := commandExecutablePath(tc.input)
			if ok != tc.wantOK {
				t.Fatalf("expected ok=%t, got %t for input %q", tc.wantOK, ok, tc.input)
			}
			if got != tc.want {
				t.Fatalf("expected executable %q, got %q", tc.want, got)
			}
		})
	}
}

func TestCommandMatchesExecutableUsesSameFileIdentity(t *testing.T) {
	tmpDir := t.TempDir()
	realBinary := filepath.Join(tmpDir, "distil-proxy-real")
	if err := os.WriteFile(realBinary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write real executable: %v", err)
	}
	hardLinkBinary := filepath.Join(tmpDir, "distil-proxy-hardlink")
	if err := os.Link(realBinary, hardLinkBinary); err != nil {
		t.Fatalf("create hard link: %v", err)
	}

	if !commandMatchesExecutable(hardLinkBinary+" __run", realBinary) {
		t.Fatal("expected hard-linked executable paths to match by file identity")
	}
}

func TestCommandMatchesExecutableGuardBranches(t *testing.T) {
	if commandMatchesExecutable("command", "   ") {
		t.Fatal("expected blank expectedPath to be rejected")
	}
	if commandMatchesExecutable(`"/tmp/unclosed`, "/tmp/distil-proxy") {
		t.Fatal("expected malformed quoted command line to be rejected")
	}
}

func TestExecutableIdentityBranches(t *testing.T) {
	if _, _, err := executableIdentity("   "); err == nil {
		t.Fatal("expected empty path error")
	}

	if _, _, err := executableIdentity(filepath.Join(t.TempDir(), "missing-binary")); err == nil {
		t.Fatal("expected stat error for missing path")
	}

	tmpDir := t.TempDir()
	realBinary := filepath.Join(tmpDir, "distil-proxy-real")
	if err := os.WriteFile(realBinary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write real executable: %v", err)
	}
	aliasPath := filepath.Join(tmpDir, "distil-proxy-alias")
	if err := os.Symlink(realBinary, aliasPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	resolved, info, err := executableIdentity(aliasPath)
	if err != nil {
		t.Fatalf("resolve executable identity: %v", err)
	}
	expectedResolved, err := filepath.EvalSymlinks(realBinary)
	if err != nil {
		t.Fatalf("resolve expected path: %v", err)
	}
	if resolved != expectedResolved {
		t.Fatalf("expected resolved path %q, got %q", expectedResolved, resolved)
	}
	if info == nil {
		t.Fatal("expected file info from executable identity")
	}
}

func TestDetachProcessSessionBranches(t *testing.T) {
	detachProcessSession(nil)

	cmd := &exec.Cmd{}
	detachProcessSession(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatalf("expected Setsid to be enabled on command, got %+v", cmd.SysProcAttr)
	}

	cmdWithAttr := &exec.Cmd{SysProcAttr: &syscall.SysProcAttr{}}
	detachProcessSession(cmdWithAttr)
	if !cmdWithAttr.SysProcAttr.Setsid {
		t.Fatalf("expected existing SysProcAttr to be updated with Setsid, got %+v", cmdWithAttr.SysProcAttr)
	}
}

func TestProcessNameByPID(t *testing.T) {
	t.Run("current-pid", func(t *testing.T) {
		name, err := processNameByPID(os.Getpid())
		if err != nil {
			if strings.Contains(err.Error(), "operation not permitted") {
				t.Skipf("process lookup unavailable in sandbox: %v", err)
			}
			t.Fatalf("expected current pid name lookup to succeed, got %v", err)
		}
		if strings.TrimSpace(name) == "" {
			t.Fatalf("expected non-empty process name, got %q", name)
		}
	})

	t.Run("invalid-pid", func(t *testing.T) {
		if _, err := processNameByPID(99999999); err == nil {
			t.Fatal("expected lookup error for invalid pid")
		} else if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("process lookup unavailable in sandbox: %v", err)
		}
	})

	t.Run("empty-process-name", func(t *testing.T) {
		origLookup := processLookupCmd
		processLookupCmd = func(_ int) *exec.Cmd {
			return exec.Command("sh", "-c", "printf ''")
		}
		defer func() { processLookupCmd = origLookup }()

		_, err := processNameByPID(os.Getpid())
		if err == nil || !strings.Contains(err.Error(), "empty process name") {
			t.Fatalf("expected empty process name error, got %v", err)
		}
	})
}

func TestTerminateProcessNilAndRunning(t *testing.T) {
	terminateProcess(nil)

	cmd := exec.Command("sh", "-c", "sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep process: %v", err)
	}
	pid := cmd.Process.Pid
	if pid <= 0 {
		t.Fatalf("expected process pid > 0, got %d", pid)
	}
	t.Cleanup(func() {
		_ = exec.Command("kill", "-KILL", strconv.Itoa(pid)).Run()
	})

	terminateProcess(cmd.Process)
	if processRunning(pid) {
		t.Fatalf("expected terminated pid %d to be stopped", pid)
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

func TestReadLogTailHandlesLongLines(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}

	longLine := strings.Repeat("x", 32*1024)
	content := longLine + "\nlast-line\n"
	if err := os.WriteFile(paths.LogFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	lines, err := ReadLogTail(paths, 2)
	if err != nil {
		t.Fatalf("read tail with long lines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %#v", lines)
	}
	if lines[0] != longLine || lines[1] != "last-line" {
		t.Fatalf("unexpected tail lines: %#v", lines)
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

	expectedPath, err := execPathFunc()
	if err != nil {
		t.Fatalf("resolve executable path: %v", err)
	}
	origProcessName := processNameFn
	processNameFn = func(_ int) (string, error) {
		return expectedPath, nil
	}
	defer func() { processNameFn = origProcessName }()

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

func TestStopTreatsOwnershipChangeDuringWaitAsStopped(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := writePID(paths, 4242); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	expectedPath, err := execPathFunc()
	if err != nil {
		t.Fatalf("resolve executable path: %v", err)
	}
	origProcessName := processNameFn
	ownershipChecks := 0
	processNameFn = func(_ int) (string, error) {
		ownershipChecks++
		if ownershipChecks == 1 {
			return expectedPath, nil
		}
		return "pid-reused-by-other-process", nil
	}
	defer func() { processNameFn = origProcessName }()

	origKill := killFunc
	killCalls := 0
	killFunc = func(_ int, sig syscall.Signal) error {
		killCalls++
		if sig == 0 || sig == syscall.SIGTERM {
			return nil
		}
		t.Fatalf("unexpected signal in test: %v", sig)
		return nil
	}
	defer func() { killFunc = origKill }()

	origTimeout := stopTimeout
	origPoll := stopPoll
	stopTimeout = 5 * time.Second
	stopPoll = 5 * time.Millisecond
	defer func() {
		stopTimeout = origTimeout
		stopPoll = origPoll
	}()

	if err := Stop(paths); err != nil {
		t.Fatalf("expected ownership change to be treated as stopped, got %v", err)
	}
	if killCalls < 2 {
		t.Fatalf("expected process probe + SIGTERM calls, got %d", killCalls)
	}
	if _, statErr := os.Stat(paths.PIDFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected stale pid file removed, err=%v", statErr)
	}
}

func TestStopRejectsPIDOwnershipMismatch(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := writePID(paths, os.Getpid()); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	if err := writeStatus(paths, RuntimeStatus{PID: os.Getpid(), Running: true, WSState: "connected"}); err != nil {
		t.Fatalf("write status: %v", err)
	}

	origProcessName := processNameFn
	processNameFn = func(_ int) (string, error) {
		return "unrelated-process", nil
	}
	defer func() { processNameFn = origProcessName }()

	var signaled bool
	origKill := killFunc
	killFunc = func(_ int, sig syscall.Signal) error {
		if sig != 0 {
			signaled = true
		}
		return nil
	}
	defer func() { killFunc = origKill }()

	err := Stop(paths)
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("expected ErrNotRunning for ownership mismatch, got %v", err)
	}
	if signaled {
		t.Fatal("expected no SIGTERM when process identity does not match daemon executable")
	}
	if _, statErr := os.Stat(paths.PIDFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected pid removed after ownership mismatch, err=%v", statErr)
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

	expectedPath, err := execPathFunc()
	if err != nil {
		t.Fatalf("resolve executable path: %v", err)
	}
	origProcessName := processNameFn
	processNameFn = func(_ int) (string, error) {
		return expectedPath, nil
	}
	defer func() { processNameFn = origProcessName }()

	origKill := killFunc
	killFunc = func(_ int, sig syscall.Signal) error {
		if sig == 0 {
			return nil
		}
		return errors.New("deny signal")
	}
	defer func() { killFunc = origKill }()

	err = Stop(paths)
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

func TestStatusPreservesUptimeWhenStopped(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}

	const persistedUptime = int64(123)
	stoppedStatus := RuntimeStatus{
		Running:       false,
		WSState:       "stopped",
		StartedAt:     time.Now().UTC().Add(-2 * time.Hour),
		UpdatedAt:     time.Now().UTC().Add(-10 * time.Minute),
		UptimeSeconds: persistedUptime,
	}
	if err := writeStatus(paths, stoppedStatus); err != nil {
		t.Fatalf("write status: %v", err)
	}

	status, err := Status(paths)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Running {
		t.Fatalf("expected stopped status, got %+v", status)
	}
	if status.UptimeSeconds != persistedUptime {
		t.Fatalf("expected persisted uptime %d, got %d", persistedUptime, status.UptimeSeconds)
	}
}

func TestStatusRejectsPIDOwnershipMismatch(t *testing.T) {
	paths := config.DefaultPaths(t.TempDir())
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := writePID(paths, os.Getpid()); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	if err := writeStatus(paths, RuntimeStatus{PID: os.Getpid(), Running: true, WSState: "connected"}); err != nil {
		t.Fatalf("write status: %v", err)
	}

	origProcessName := processNameFn
	processNameFn = func(_ int) (string, error) {
		return "unrelated-process", nil
	}
	defer func() { processNameFn = origProcessName }()

	status, err := Status(paths)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Running {
		t.Fatalf("expected status to report not running for ownership mismatch: %+v", status)
	}
	if status.WSState != "stopped" {
		t.Fatalf("expected ws_state=stopped for ownership mismatch, got %+v", status)
	}
	if _, statErr := os.Stat(paths.PIDFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected stale pid file removed, err=%v", statErr)
	}
}

func TestStatusForcesStoppedStateWhenPIDNotRunning(t *testing.T) {
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

	status, err := Status(paths)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Running {
		t.Fatalf("expected status to report not running for dead pid: %+v", status)
	}
	if status.WSState != "stopped" {
		t.Fatalf("expected ws_state=stopped for dead pid, got %+v", status)
	}
	if _, statErr := os.Stat(paths.PIDFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected stale pid file removed, err=%v", statErr)
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
	if err := os.Remove(paths.LogFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove log file: %v", err)
	}
	if err := os.Mkdir(paths.LogFile, 0o700); err != nil {
		t.Fatalf("create directory at log path: %v", err)
	}

	_, err := ReadLogTail(paths, 10)
	if err == nil || !strings.Contains(err.Error(), "read log file") {
		t.Fatalf("expected read log error, got %v", err)
	}
}
