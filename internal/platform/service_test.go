package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func withPlatformGlobalsReset(t *testing.T) {
	t.Helper()
	origGOOS := platformGOOS
	origExec := execCommandFunc
	origRemove := removeFileFunc
	origMkdirAll := mkdirAllFunc
	origWriteFile := writeFileFunc
	t.Cleanup(func() {
		platformGOOS = origGOOS
		execCommandFunc = origExec
		removeFileFunc = origRemove
		mkdirAllFunc = origMkdirAll
		writeFileFunc = origWriteFile
	})
}

func TestRemoveServiceDefinitions(t *testing.T) {
	withPlatformGlobalsReset(t)
	home := t.TempDir()
	execCommandFunc = func(string, ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "true")
	}

	platformGOOS = "darwin"
	plist := filepath.Join(home, LaunchdServicePath)
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatalf("mkdir plist dir: %v", err)
	}
	if err := os.WriteFile(plist, []byte("plist"), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	if err := RemoveServiceDefinitions(home); err != nil {
		t.Fatalf("remove service defs darwin: %v", err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatalf("expected plist removed, err=%v", err)
	}

	platformGOOS = "linux"
	service := filepath.Join(home, SystemdServicePath)
	if err := os.MkdirAll(filepath.Dir(service), 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := os.WriteFile(service, []byte("unit"), 0o644); err != nil {
		t.Fatalf("write service: %v", err)
	}
	if err := RemoveServiceDefinitions(home); err != nil {
		t.Fatalf("remove service defs linux: %v", err)
	}
	if _, err := os.Stat(service); !os.IsNotExist(err) {
		t.Fatalf("expected systemd service removed, err=%v", err)
	}
}

func TestRemoveServiceDefinitionsErrors(t *testing.T) {
	withPlatformGlobalsReset(t)
	home := t.TempDir()

	platformGOOS = "darwin"
	removeFileFunc = func(string) error { return errors.New("remove failed") }

	err := RemoveServiceDefinitions(home)
	if err == nil || !strings.Contains(err.Error(), "remove") {
		t.Fatalf("expected remove error, got %v", err)
	}
}

func TestRemoveIfExists(t *testing.T) {
	withPlatformGlobalsReset(t)

	if err := removeIfExists(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("expected nil on missing file, got %v", err)
	}

	removeFileFunc = func(string) error { return errors.New("bad remove") }
	if err := removeIfExists("x"); err == nil {
		t.Fatal("expected remove error")
	}
}

func TestInstallServiceDefinition(t *testing.T) {
	withPlatformGlobalsReset(t)
	home := t.TempDir()

	execCommandFunc = func(name string, args ...string) *exec.Cmd {
		_ = name
		_ = args
		return exec.Command("sh", "-c", "true")
	}

	platformGOOS = "darwin"
	if err := InstallServiceDefinition(home); err != nil {
		t.Fatalf("install launchd service: %v", err)
	}
	plist := filepath.Join(home, LaunchdServicePath)
	if _, err := os.Stat(plist); err != nil {
		t.Fatalf("expected launchd plist written: %v", err)
	}

	platformGOOS = "linux"
	if err := InstallServiceDefinition(home); err != nil {
		t.Fatalf("install systemd service: %v", err)
	}
	service := filepath.Join(home, SystemdServicePath)
	if _, err := os.Stat(service); err != nil {
		t.Fatalf("expected systemd service written: %v", err)
	}
}

func TestInstallServiceDefinitionErrors(t *testing.T) {
	t.Run("unsupported-os", func(t *testing.T) {
		withPlatformGlobalsReset(t)
		platformGOOS = "plan9"
		if err := InstallServiceDefinition(t.TempDir()); err == nil {
			t.Fatal("expected unsupported os error")
		}
	})

	t.Run("mkdir-error", func(t *testing.T) {
		withPlatformGlobalsReset(t)
		platformGOOS = "darwin"
		mkdirAllFunc = func(string, os.FileMode) error { return errors.New("mkdir failed") }
		err := InstallServiceDefinition(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "create launchd directory") {
			t.Fatalf("expected mkdir error, got %v", err)
		}
	})

	t.Run("write-error", func(t *testing.T) {
		withPlatformGlobalsReset(t)
		platformGOOS = "linux"
		writeFileFunc = func(string, []byte, os.FileMode) error { return errors.New("write failed") }
		err := InstallServiceDefinition(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "write systemd unit") {
			t.Fatalf("expected write error, got %v", err)
		}
	})

	t.Run("launchctl-load-error", func(t *testing.T) {
		withPlatformGlobalsReset(t)
		platformGOOS = "darwin"
		execCommandFunc = func(name string, args ...string) *exec.Cmd {
			if len(args) > 0 && args[0] == "load" {
				return exec.Command("sh", "-c", "false")
			}
			return exec.Command("sh", "-c", "true")
		}
		err := InstallServiceDefinition(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "load launchd service") {
			t.Fatalf("expected launchctl load error, got %v", err)
		}
	})

	t.Run("systemctl-daemon-reload-error", func(t *testing.T) {
		withPlatformGlobalsReset(t)
		platformGOOS = "linux"
		execCommandFunc = func(name string, args ...string) *exec.Cmd {
			if len(args) >= 2 && args[0] == "--user" && args[1] == "daemon-reload" {
				return exec.Command("sh", "-c", "false")
			}
			return exec.Command("sh", "-c", "true")
		}
		err := InstallServiceDefinition(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "daemon-reload") {
			t.Fatalf("expected daemon-reload error, got %v", err)
		}
	})

	t.Run("systemctl-enable-error", func(t *testing.T) {
		withPlatformGlobalsReset(t)
		platformGOOS = "linux"
		execCommandFunc = func(name string, args ...string) *exec.Cmd {
			if len(args) >= 2 && args[0] == "--user" && args[1] == "enable" {
				return exec.Command("sh", "-c", "false")
			}
			return exec.Command("sh", "-c", "true")
		}
		err := InstallServiceDefinition(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "enable systemd service") {
			t.Fatalf("expected enable error, got %v", err)
		}
	})
}
