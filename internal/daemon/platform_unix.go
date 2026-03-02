//go:build !windows

package daemon

import (
	"errors"
	"os/exec"
	"strconv"
	"syscall"
)

const (
	sigTerm = syscall.SIGTERM
	sigZero = syscall.Signal(0)
)

func signalProcess(pid int, signal syscall.Signal) error {
	return syscall.Kill(pid, signal)
}

func isNoSuchProcess(err error) bool {
	return errors.Is(err, syscall.ESRCH)
}

func isPermissionError(err error) bool {
	return errors.Is(err, syscall.EPERM)
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

func execReplace(path string, args []string, env []string) error {
	return syscall.Exec(path, args, env)
}

func processLookupCommand(pid int) *exec.Cmd {
	return exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=")
}
