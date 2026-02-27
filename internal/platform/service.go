package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

var (
	platformGOOS      = runtime.GOOS
	execCommandFunc   = exec.Command
	removeFileFunc    = os.Remove
	mkdirAllFunc      = os.MkdirAll
	writeFileFunc     = os.WriteFile
)

// RemoveServiceDefinitions removes local launchd/systemd-user service definitions.
func RemoveServiceDefinitions(homeDir string) error {
	var errs []error

	switch platformGOOS {
	case "darwin":
		plist := filepath.Join(homeDir, LaunchdServicePath)
		_ = execCommandFunc("launchctl", "unload", plist).Run()
		if err := removeIfExists(plist); err != nil {
			errs = append(errs, err)
		}
	case "linux":
		service := filepath.Join(homeDir, SystemdServicePath)
		_ = execCommandFunc("systemctl", "--user", "disable", "--now", "distil-proxy").Run()
		_ = execCommandFunc("systemctl", "--user", "daemon-reload").Run()
		if err := removeIfExists(service); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func removeIfExists(path string) error {
	if err := removeFileFunc(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
