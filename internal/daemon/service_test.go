package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/exec-io/distil-proxy/internal/config"
	"github.com/exec-io/distil-proxy/internal/ws"
)

type fakeRunner struct {
	run func(ctx context.Context) error
}

func (f fakeRunner) Run(ctx context.Context) error {
	if f.run != nil {
		return f.run(ctx)
	}
	return nil
}

func TestStatusNoFiles(t *testing.T) {
	home := t.TempDir()
	paths := config.DefaultPaths(home)

	status, err := Status(paths)
	if err != nil {
		t.Fatalf("status error: %v", err)
	}
	if status.Running {
		t.Fatal("expected running=false")
	}
	if status.WSState != "stopped" {
		t.Fatalf("expected ws_state=stopped, got %s", status.WSState)
	}
}

func TestReadLogTail(t *testing.T) {
	home := t.TempDir()
	paths := config.DefaultPaths(home)
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}

	if err := os.WriteFile(paths.LogFile, []byte("a\nb\nc\nd\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	lines, err := ReadLogTail(paths, 2)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	if len(lines) != 2 || lines[0] != "c" || lines[1] != "d" {
		t.Fatalf("unexpected tail: %#v", lines)
	}
}

func TestStopNotRunning(t *testing.T) {
	home := t.TempDir()
	paths := config.DefaultPaths(home)

	err := Stop(paths)
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("expected ErrNotRunning, got %v", err)
	}
}

func TestStatusReflectsRunningPID(t *testing.T) {
	home := t.TempDir()
	paths := config.DefaultPaths(home)
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}

	pid := os.Getpid()
	if err := os.WriteFile(paths.PIDFile, []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
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

	status, err := Status(paths)
	if err != nil {
		t.Fatalf("status error: %v", err)
	}
	if !status.Running {
		t.Fatal("expected running=true")
	}
	if status.PID != pid {
		t.Fatalf("expected pid=%d, got %d", pid, status.PID)
	}
}

func TestRunClientPanicRecovery(t *testing.T) {
	home := t.TempDir()
	paths := config.DefaultPaths(home)
	cfg := config.Config{APIKey: "dk_test", Server: "wss://proxy.distil.net/ws", TimeoutMS: 1000, LogLevel: "info"}

	originalFactory := clientFactory
	clientFactory = func(_ ws.ClientConfig) (wsRunner, error) {
		return fakeRunner{run: func(context.Context) error {
			panic("boom")
		}}, nil
	}
	defer func() { clientFactory = originalFactory }()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err := Run(context.Background(), paths, cfg, logger)
	if err == nil {
		t.Fatal("expected panic recovery error")
	}
	if !strings.Contains(err.Error(), "panic in websocket runtime") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunGracefulShutdown(t *testing.T) {
	home := t.TempDir()
	paths := config.DefaultPaths(home)
	cfg := config.Config{APIKey: "dk_test", Server: "wss://proxy.distil.net/ws", TimeoutMS: 1000, LogLevel: "info"}

	originalFactory := clientFactory
	clientFactory = func(_ ws.ClientConfig) (wsRunner, error) {
		return fakeRunner{run: func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		}}, nil
	}
	defer func() { clientFactory = originalFactory }()

	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, paths, cfg, logger)
	}()

	for i := 0; i < 100; i++ {
		if _, err := os.Stat(paths.PIDFile); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for run to exit")
	}

	if _, err := os.Stat(paths.PIDFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected pid file removed, err=%v", err)
	}

	data, err := os.ReadFile(paths.StatusFile)
	if err != nil {
		t.Fatalf("read status file: %v", err)
	}
	if !strings.Contains(string(data), "\"ws_state\": \"stopped\"") {
		t.Fatalf("unexpected status content: %s", string(data))
	}
}
