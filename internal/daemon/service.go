package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/exec-io/distil-proxy/internal/config"
	"github.com/exec-io/distil-proxy/internal/fetch"
	"github.com/exec-io/distil-proxy/internal/observability"
	"github.com/exec-io/distil-proxy/internal/ws"
)

var (
	// ErrNotRunning is returned when a stop operation is requested while daemon is offline.
	ErrNotRunning = errors.New("daemon is not running")

	clientFactory    = newWSClient
	execPathFunc     = os.Executable
	execCmdFunc      = exec.Command
	stopTimeout      = 10 * time.Second
	stopPoll         = 200 * time.Millisecond
	statusTick       = 5 * time.Second
	killFunc         = syscall.Kill
	marshalStatus    = json.MarshalIndent
	processNameFn    = processNameByPID
	processLookupCmd = func(pid int) *exec.Cmd {
		return exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=")
	}
)

type wsRunner interface {
	Run(ctx context.Context) error
}

func newWSClient(cfg ws.ClientConfig) (wsRunner, error) {
	return ws.NewClient(cfg)
}

// RuntimeStatus is persisted to disk for the status command.
type RuntimeStatus struct {
	PID           int       `json:"pid"`
	Running       bool      `json:"running"`
	WSState       string    `json:"ws_state"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastHeartbeat time.Time `json:"last_heartbeat,omitempty"`
	JobsServed    int64     `json:"jobs_served"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	LastError     string    `json:"last_error,omitempty"`

	ConnectAttempts int64 `json:"connect_attempts"`
	Reconnects      int64 `json:"reconnects"`
	JobsSuccess     int64 `json:"jobs_success"`
	JobsError       int64 `json:"jobs_error"`
	AvgLatencyMS    int64 `json:"avg_latency_ms"`
	LatencyLE100MS  int64 `json:"latency_le_100_ms"`
	LatencyLE500MS  int64 `json:"latency_le_500_ms"`
	LatencyLE1000MS int64 `json:"latency_le_1000_ms"`
	LatencyGT1000MS int64 `json:"latency_gt_1000_ms"`
}

// Start launches the daemon as a detached child process and writes runtime state files.
func Start(paths config.Paths, cfg config.Config) error {
	if err := config.EnsureStateDirs(paths); err != nil {
		return err
	}

	if pid, ok := runningPID(paths); ok {
		return fmt.Errorf("daemon already running with pid %d", pid)
	}

	execPath, err := execPathFunc()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	logFile, err := os.OpenFile(paths.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer logFile.Close()

	cmd := execCmdFunc(execPath, "__run")
	cmd.Env = append(os.Environ(), "DISTIL_PROXY_DAEMON=1")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon child: %w", err)
	}

	if err := writePID(paths, cmd.Process.Pid); err != nil {
		terminateProcess(cmd.Process)
		return err
	}

	now := time.Now().UTC()
	status := RuntimeStatus{
		PID:           cmd.Process.Pid,
		Running:       true,
		WSState:       "starting",
		StartedAt:     now,
		UpdatedAt:     now,
		JobsServed:    0,
		UptimeSeconds: 0,
	}
	if err := writeStatus(paths, status); err != nil {
		_ = removePID(paths)
		terminateProcess(cmd.Process)
		return err
	}

	_ = cfg // consumed in future phases; config is already validated by caller.
	return nil
}

// StartForeground runs the daemon loop in the current process.
func StartForeground(ctx context.Context, paths config.Paths, cfg config.Config, logWriter io.Writer) error {
	if err := config.EnsureStateDirs(paths); err != nil {
		return err
	}
	if pid, ok := runningPID(paths); ok {
		return fmt.Errorf("daemon already running with pid %d", pid)
	}

	logger, err := observability.NewLogger(cfg.LogLevel, logWriter)
	if err != nil {
		return err
	}

	observability.Log(ctx, logger, slog.LevelInfo, "daemon foreground mode", observability.EventFields{
		Event:      "daemon_foreground",
		DurationMS: 0,
	})
	return Run(ctx, paths, cfg, logger)
}

// Run executes the daemon loop in the detached child process.
func Run(ctx context.Context, paths config.Paths, cfg config.Config, logger *slog.Logger) error {
	if err := config.EnsureStateDirs(paths); err != nil {
		return err
	}

	pid := os.Getpid()
	startedAt := time.Now().UTC()
	status := RuntimeStatus{
		PID:           pid,
		Running:       true,
		WSState:       "disconnected",
		StartedAt:     startedAt,
		UpdatedAt:     startedAt,
		JobsServed:    0,
		UptimeSeconds: 0,
	}
	if err := writePID(paths, pid); err != nil {
		return err
	}
	if err := writeStatus(paths, status); err != nil {
		return err
	}

	observability.Log(ctx, logger, slog.LevelInfo, "daemon process started", observability.EventFields{
		Event:      "daemon_started",
		DurationMS: 0,
		JobID:      "",
	})

	var statusMu sync.Mutex
	metrics := &observability.Metrics{}
	updateStatus := func(mutator func(*RuntimeStatus)) {
		statusMu.Lock()
		defer statusMu.Unlock()
		mutator(&status)
		snapshot := metrics.Snapshot()
		status.ConnectAttempts = snapshot.ConnectAttempts
		status.Reconnects = snapshot.Reconnects
		status.JobsSuccess = snapshot.JobsSuccess
		status.JobsError = snapshot.JobsError
		status.AvgLatencyMS = snapshot.AvgLatencyMS
		status.LatencyLE100MS = snapshot.LatencyLE100MS
		status.LatencyLE500MS = snapshot.LatencyLE500MS
		status.LatencyLE1000MS = snapshot.LatencyLE1000MS
		status.LatencyGT1000MS = snapshot.LatencyGT1000MS
		status.JobsServed = snapshot.JobsSuccess
		status.UpdatedAt = time.Now().UTC()
		status.UptimeSeconds = int64(time.Since(startedAt).Seconds())
		if err := writeStatus(paths, status); err != nil {
			logger.Error("failed to write runtime status", "error", err)
		}
	}

	client, err := clientFactory(ws.ClientConfig{
		ServerURL:        cfg.Server,
		APIKey:           cfg.APIKey,
		ProtocolVersion:  ws.DefaultProtocolVersion,
		DefaultTimeoutMS: cfg.TimeoutMS,
		Fetcher:          fetch.NewHTTPExecutor(fetch.DefaultMaxBodyBytes),
		Logger:           logger,
		Hooks: ws.Hooks{
			OnStateChange: func(state string) {
				if state == "connected" {
					metrics.IncConnectAttempts()
				}
				if state == "reconnecting" {
					metrics.IncReconnects()
				}
				updateStatus(func(s *RuntimeStatus) {
					s.WSState = state
				})
			},
			OnHeartbeat: func(at time.Time) {
				updateStatus(func(s *RuntimeStatus) {
					s.LastHeartbeat = at
				})
			},
			OnJobResult: func(success bool, durationMS int64) {
				metrics.RecordJob(success, durationMS)
				updateStatus(func(_ *RuntimeStatus) {})
			},
			OnError: func(err error) {
				updateStatus(func(s *RuntimeStatus) {
					s.LastError = err.Error()
				})
			},
		},
	})
	if err != nil {
		return err
	}

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	ticker := time.NewTicker(statusTick)
	defer ticker.Stop()
	tickerDone := make(chan struct{})
	go func() {
		defer close(tickerDone)
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				updateStatus(func(_ *RuntimeStatus) {})
			}
		}
	}()

	runErr := runClientWithRecovery(runCtx, client)
	runCancel()
	<-tickerDone
	if runErr != nil {
		updateStatus(func(s *RuntimeStatus) {
			s.LastError = runErr.Error()
		})
		return runErr
	}

	statusMu.Lock()
	status.Running = false
	status.WSState = "stopped"
	status.UpdatedAt = time.Now().UTC()
	status.UptimeSeconds = int64(time.Since(startedAt).Seconds())
	writeErr := writeStatus(paths, status)
	statusMu.Unlock()
	if writeErr != nil {
		return writeErr
	}
	if err := removePIDIfMatches(paths, pid); err != nil {
		return err
	}
	observability.Log(ctx, logger, slog.LevelInfo, "daemon process stopped", observability.EventFields{
		Event:      "daemon_stopped",
		DurationMS: 0,
		JobID:      "",
	})
	return nil
}

func runClientWithRecovery(ctx context.Context, client wsRunner) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in websocket runtime: %v", r)
		}
	}()
	return client.Run(ctx)
}

// Stop signals the daemon process and waits for termination.
func Stop(paths config.Paths) error {
	pid, err := readPID(paths)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotRunning
		}
		return err
	}

	if !processRunning(pid) {
		if err := removePID(paths); err != nil {
			return err
		}
		markStopped(paths, pid, "stopped")
		return ErrNotRunning
	}
	if !daemonOwnsPID(pid) {
		if err := removePID(paths); err != nil {
			return err
		}
		markStopped(paths, pid, "stopped")
		return ErrNotRunning
	}

	if err := killFunc(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal daemon pid %d: %w", pid, err)
	}

	deadline := time.Now().Add(stopTimeout)
	for {
		if !processRunning(pid) || !daemonOwnsPID(pid) {
			if err := removePID(paths); err != nil {
				return err
			}
			markStopped(paths, pid, "stopped")
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(stopPoll)
	}

	return fmt.Errorf("timed out waiting for pid %d to stop", pid)
}

// Restart performs stop (if running) followed by start.
func Restart(paths config.Paths, cfg config.Config) error {
	if err := Stop(paths); err != nil && !errors.Is(err, ErrNotRunning) {
		return err
	}
	return Start(paths, cfg)
}

// Status returns the best-known runtime state for CLI consumption.
func Status(paths config.Paths) (RuntimeStatus, error) {
	status, err := readStatus(paths)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return RuntimeStatus{}, err
		}
		status = RuntimeStatus{WSState: "stopped"}
	}

	pid, err := readPID(paths)
	if err == nil {
		status.PID = pid
		status.Running = processRunning(pid) && daemonOwnsPID(pid)
		if !status.Running {
			_ = removePID(paths)
			if status.WSState == "" {
				status.WSState = "stopped"
			}
		}
	} else if errors.Is(err, os.ErrNotExist) {
		status.Running = false
		if status.WSState == "" {
			status.WSState = "stopped"
		}
	} else {
		return RuntimeStatus{}, err
	}

	if status.Running && !status.StartedAt.IsZero() {
		status.UptimeSeconds = int64(time.Since(status.StartedAt).Seconds())
	}
	if status.UpdatedAt.IsZero() {
		status.UpdatedAt = time.Now().UTC()
	}

	return status, nil
}

func markStopped(paths config.Paths, pid int, wsState string) {
	status, err := readStatus(paths)
	if err != nil {
		status = RuntimeStatus{}
	}
	status.PID = pid
	status.Running = false
	status.WSState = wsState
	status.UpdatedAt = time.Now().UTC()
	_ = writeStatus(paths, status)
}

func runningPID(paths config.Paths) (int, bool) {
	pid, err := readPID(paths)
	if err != nil {
		return 0, false
	}
	if !processRunning(pid) {
		_ = removePID(paths)
		return 0, false
	}
	if !daemonOwnsPID(pid) {
		_ = removePID(paths)
		return 0, false
	}

	return pid, true
}

func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}

	err := killFunc(pid, syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

func daemonOwnsPID(pid int) bool {
	if pid <= 0 {
		return false
	}
	execPath, err := execPathFunc()
	if err != nil {
		return false
	}
	expectedName := filepath.Base(strings.TrimSpace(execPath))
	if expectedName == "" {
		return false
	}
	processName, err := processNameFn(pid)
	if err != nil {
		return false
	}
	return filepath.Base(strings.TrimSpace(processName)) == expectedName
}

func processNameByPID(pid int) (string, error) {
	cmd := processLookupCmd(pid)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read process name: %w", err)
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		return "", errors.New("empty process name")
	}
	return name, nil
}

func terminateProcess(proc *os.Process) {
	if proc == nil {
		return
	}
	_ = proc.Kill()
	_, _ = proc.Wait()
}

func writePID(paths config.Paths, pid int) error {
	if err := config.EnsureStateDirs(paths); err != nil {
		return err
	}
	content := []byte(fmt.Sprintf("%d\n", pid))
	if err := os.WriteFile(paths.PIDFile, content, 0o600); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}
	return nil
}

func readPID(paths config.Paths) (int, error) {
	content, err := os.ReadFile(paths.PIDFile)
	if err != nil {
		return 0, err
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil {
		return 0, fmt.Errorf("parse pid file: %w", err)
	}

	return pid, nil
}

func removePID(paths config.Paths) error {
	if err := os.Remove(paths.PIDFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove pid file: %w", err)
	}
	return nil
}

func removePIDIfMatches(paths config.Paths, expectedPID int) error {
	pid, err := readPID(paths)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if pid != expectedPID {
		return nil
	}
	return removePID(paths)
}

func writeStatus(paths config.Paths, status RuntimeStatus) error {
	if err := config.EnsureStateDirs(paths); err != nil {
		return err
	}

	payload, err := marshalStatus(status, "", "  ")
	if err != nil {
		return fmt.Errorf("encode status: %w", err)
	}
	payload = append(payload, '\n')

	tmp := paths.StatusFile + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return fmt.Errorf("write temp status: %w", err)
	}
	if err := os.Rename(tmp, paths.StatusFile); err != nil {
		return fmt.Errorf("replace status file: %w", err)
	}

	return nil
}

func readStatus(paths config.Paths) (RuntimeStatus, error) {
	content, err := os.ReadFile(paths.StatusFile)
	if err != nil {
		return RuntimeStatus{}, err
	}
	var status RuntimeStatus
	if err := json.Unmarshal(content, &status); err != nil {
		return RuntimeStatus{}, fmt.Errorf("decode status: %w", err)
	}
	return status, nil
}

// ReadLogTail returns up to n most recent lines from the daemon log file.
func ReadLogTail(paths config.Paths, n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}

	f, err := os.Open(paths.LogFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	lines := make([]string, n)
	count := 0
	reader := bufio.NewReader(f)
	var lineBuilder strings.Builder
	for {
		chunk, isPrefix, err := reader.ReadLine()
		if len(chunk) > 0 {
			_, _ = lineBuilder.Write(chunk)
		}
		if err == nil && isPrefix {
			continue
		}
		if err == nil {
			lines[count%n] = lineBuilder.String()
			count++
			lineBuilder.Reset()
			continue
		}
		if errors.Is(err, io.EOF) {
			if lineBuilder.Len() > 0 {
				lines[count%n] = lineBuilder.String()
				count++
			}
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read log file: %w", err)
		}
	}

	if count == 0 {
		return []string{}, nil
	}

	if count <= n {
		return trimTrailingEmptyLogLines(append([]string(nil), lines[:count]...)), nil
	}

	tail := make([]string, n)
	start := count % n
	for i := 0; i < n; i++ {
		tail[i] = lines[(start+i)%n]
	}
	return trimTrailingEmptyLogLines(tail), nil
}

func trimTrailingEmptyLogLines(lines []string) []string {
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
