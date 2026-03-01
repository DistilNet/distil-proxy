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
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/exec-io/distil-proxy/internal/config"
	"github.com/exec-io/distil-proxy/internal/fetch"
	"github.com/exec-io/distil-proxy/internal/observability"
	"github.com/exec-io/distil-proxy/internal/upgrade"
	"github.com/exec-io/distil-proxy/internal/version"
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
		return exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=")
	}
	exitFunc              = os.Exit
	upgradeManagerFactory = newUpgradeManager
)

type wsRunner interface {
	Run(ctx context.Context) error
}

type upgradeManager interface {
	HandleStartup() (bool, error)
	CheckInterval() time.Duration
	CheckAndUpgrade(ctx context.Context) (upgrade.CheckResult, error)
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
	detachProcessSession(cmd)
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

	upgrader := upgradeManagerFactory(paths, cfg)
	if upgrader != nil {
		rolledBack, err := upgrader.HandleStartup()
		if err != nil {
			updateStatus(func(s *RuntimeStatus) {
				s.LastError = fmt.Sprintf("auto-upgrade startup check failed: %v", err)
			})
		} else if rolledBack {
			updateStatus(func(s *RuntimeStatus) {
				s.WSState = "restarting"
			})
			return restartProcess()
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

	var upgradeRequested atomic.Bool
	if upgrader != nil {
		go func() {
			upgradeTicker := time.NewTicker(upgrader.CheckInterval())
			defer upgradeTicker.Stop()

			for {
				select {
				case <-runCtx.Done():
					return
				case <-upgradeTicker.C:
					result, err := upgrader.CheckAndUpgrade(runCtx)
					if err != nil {
						updateStatus(func(s *RuntimeStatus) {
							s.LastError = fmt.Sprintf("auto-upgrade check failed: %v", err)
						})
						continue
					}
					if result.Applied {
						updateStatus(func(s *RuntimeStatus) {
							s.WSState = "restarting"
						})
						upgradeRequested.Store(true)
						runCancel()
						return
					}
				}
			}
		}()
	}

	runErr := runClientWithRecovery(runCtx, client)
	runCancel()
	<-tickerDone
	if upgradeRequested.Load() {
		return restartProcess()
	}
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

func newUpgradeManager(paths config.Paths, cfg config.Config) upgradeManager {
	if !cfg.AutoUpgrade {
		return nil
	}

	execPath, err := execPathFunc()
	if err != nil {
		return nil
	}

	checkInterval := time.Duration(cfg.UpgradeCheckHours) * time.Hour
	if checkInterval <= 0 {
		checkInterval = time.Duration(config.DefaultUpgradeCheckHours) * time.Hour
	}

	return upgrade.NewManager(upgrade.ManagerConfig{
		Enabled:        cfg.AutoUpgrade,
		APIKey:         cfg.APIKey,
		CurrentVersion: version.DefaultInfo().Version,
		BinaryPath:     execPath,
		TempBinaryPath: execPath + ".new",
		BackupPath:     execPath + ".bak",
		StatePath:      paths.UpgradeStateFile,
		CheckInterval:  checkInterval,
	})
}

func restartProcess() error {
	execPath, err := execPathFunc()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	cmd := execCmdFunc(execPath, os.Args[1:]...)
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restart process: %w", err)
	}
	exitFunc(0)
	return nil
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
		if errors.Is(err, syscall.ESRCH) {
			if err := removePID(paths); err != nil {
				return err
			}
			markStopped(paths, pid, "stopped")
			return nil
		}
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
			status.WSState = "stopped"
		}
	} else if errors.Is(err, os.ErrNotExist) {
		status.Running = false
		status.WSState = "stopped"
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
	expectedPath := strings.TrimSpace(execPath)
	if expectedPath == "" {
		return false
	}
	processCommand, err := processNameFn(pid)
	if err != nil {
		return false
	}
	return commandMatchesExecutable(processCommand, expectedPath)
}

func commandMatchesExecutable(commandLine, expectedPath string) bool {
	commandLine = strings.TrimSpace(commandLine)
	expectedPath = strings.TrimSpace(expectedPath)
	if commandLine == "" || expectedPath == "" {
		return false
	}

	candidates := make([]string, 0, 2)
	if commandPath, ok := commandExecutableForDaemonRun(commandLine); ok {
		candidates = append(candidates, commandPath)
	}
	if commandPath, ok := commandExecutableForForegroundStart(commandLine); ok {
		candidates = append(candidates, commandPath)
	}
	for _, commandPath := range candidates {
		if commandPath == expectedPath || sameExecutableFile(commandPath, expectedPath) {
			return true
		}
		if normalized, ok := normalizeRelativeCommandPath(commandPath, expectedPath); ok {
			if normalized == expectedPath || sameExecutableFile(normalized, expectedPath) {
				return true
			}
		}
	}
	return false
}

func normalizeRelativeCommandPath(commandPath, expectedPath string) (string, bool) {
	commandPath = strings.TrimSpace(commandPath)
	expectedPath = strings.TrimSpace(expectedPath)
	if commandPath == "" || expectedPath == "" || filepath.IsAbs(commandPath) {
		return "", false
	}
	return filepath.Clean(filepath.Join(filepath.Dir(expectedPath), commandPath)), true
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

func commandExecutablePath(commandLine string) (string, bool) {
	commandLine = strings.TrimSpace(commandLine)
	if commandLine == "" {
		return "", false
	}
	if commandLine[0] == '"' || commandLine[0] == '\'' {
		return quotedExecutablePath(commandLine)
	}
	if idx := strings.IndexFunc(commandLine, unicode.IsSpace); idx >= 0 {
		return commandLine[:idx], true
	}
	return commandLine, true
}

func commandExecutableForDaemonRun(commandLine string) (string, bool) {
	commandLine = strings.TrimSpace(commandLine)
	if commandLine == "" {
		return "", false
	}

	const daemonRunArg = "__run"
	idx := strings.LastIndex(commandLine, daemonRunArg)
	if idx <= 0 {
		return "", false
	}
	if strings.TrimSpace(commandLine[idx+len(daemonRunArg):]) != "" {
		return "", false
	}

	rawPrefix := commandLine[:idx]
	lastRune, _ := utf8.DecodeLastRuneInString(rawPrefix)
	if !unicode.IsSpace(lastRune) {
		return "", false
	}
	commandPath := strings.TrimSpace(rawPrefix)
	if unquoted, ok := unquotePath(commandPath); ok {
		commandPath = unquoted
	}
	if commandPath == "" {
		return "", false
	}
	return commandPath, true
}

func commandExecutableForForegroundStart(commandLine string) (string, bool) {
	commandLine = strings.TrimSpace(commandLine)
	if commandLine == "" {
		return "", false
	}

	suffixes := []string{
		" start --foreground",
		" start --foreground=true",
	}
	for _, suffix := range suffixes {
		if !strings.HasSuffix(commandLine, suffix) {
			continue
		}
		commandPath := strings.TrimSpace(strings.TrimSuffix(commandLine, suffix))
		if unquoted, ok := unquotePath(commandPath); ok {
			commandPath = unquoted
		}
		if commandPath == "" {
			return "", false
		}
		return commandPath, true
	}

	return "", false
}

func quotedExecutablePath(commandLine string) (string, bool) {
	quote := commandLine[0]
	var builder strings.Builder
	escaped := false
	for i := 1; i < len(commandLine); i++ {
		ch := commandLine[i]
		if quote == '"' && ch == '\\' && !escaped {
			escaped = true
			continue
		}
		if escaped {
			builder.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == quote {
			if builder.Len() == 0 {
				return "", false
			}
			return builder.String(), true
		}
		builder.WriteByte(ch)
	}
	return "", false
}

func unquotePath(path string) (string, bool) {
	if len(path) < 2 {
		return "", false
	}
	quote := path[0]
	if (quote != '"' && quote != '\'') || path[len(path)-1] != quote {
		return "", false
	}
	return path[1 : len(path)-1], true
}

func executableIdentity(path string) (string, os.FileInfo, error) {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" || cleaned == "." {
		return "", nil, errors.New("empty executable path")
	}

	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		resolved = cleaned
	}
	resolved = filepath.Clean(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, err
	}
	return resolved, info, nil
}

func sameExecutableFile(pathA, pathB string) bool {
	resolvedA, infoA, errA := executableIdentity(pathA)
	resolvedB, infoB, errB := executableIdentity(pathB)
	if errA != nil || errB != nil {
		return false
	}
	if resolvedA == resolvedB {
		return true
	}
	return os.SameFile(infoA, infoB)
}

func detachProcessSession(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
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
