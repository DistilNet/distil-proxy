//go:build windows

package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

const (
	sigTerm = syscall.Signal(15)
	sigZero = syscall.Signal(0)
)

var errNoSuchProcess = errors.New("process not found")

func signalProcess(pid int, signal syscall.Signal) error {
	if signal == sigZero {
		return checkProcessExists(pid)
	}

	if signal == sigTerm {
		cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
		out, err := cmd.CombinedOutput()
		if err != nil {
			if noSuchProcessOutput(out) {
				return errNoSuchProcess
			}
			return err
		}
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(signal)
}

func checkProcessExists(pid int) error {
	cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	if strings.Contains(string(out), fmt.Sprintf(",\"%d\",", pid)) {
		return nil
	}
	return errNoSuchProcess
}

func noSuchProcessOutput(out []byte) bool {
	text := strings.ToLower(string(out))
	return strings.Contains(text, "not found") || strings.Contains(text, "no running instance")
}

func isNoSuchProcess(err error) bool {
	return errors.Is(err, errNoSuchProcess) || errors.Is(err, os.ErrProcessDone)
}

func isPermissionError(_ error) bool {
	return false
}

func detachProcessSession(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
}

func execReplace(path string, args []string, env []string) error {
	// Windows does not support exec-style process replacement.
	// Instead, spawn a new detached process and exit the current one.
	cmd := exec.Command(path, args[1:]...)
	cmd.Env = env
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	// CREATE_NEW_PROCESS_GROUP detaches from the current console group
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start replacement process: %w", err)
	}
	// Exit current process to complete the replacement
	os.Exit(0)
	return nil // unreachable
}

func processLookupCommand(pid int) *exec.Cmd {
	// Use WMIC to get the command line for a process by PID on Windows
	return exec.Command("wmic", "process", "where", fmt.Sprintf("ProcessId=%d", pid), "get", "CommandLine", "/value")
}
