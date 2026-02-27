package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exec-io/distil-proxy/internal/config"
	"github.com/exec-io/distil-proxy/internal/version"
)

func runCLI(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("HOME", home)

	cmd := NewRootCmd(version.DefaultInfo())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)

	err := cmd.Execute()
	return out.String(), err
}

func TestAuthCommand(t *testing.T) {
	home := t.TempDir()
	out, err := runCLI(t, home, "auth", "dk_auth_test")
	if err != nil {
		t.Fatalf("auth command error: %v", err)
	}
	if !strings.Contains(out, "updated config") {
		t.Fatalf("unexpected output: %q", out)
	}

	paths := config.DefaultPaths(home)
	cfg, err := config.Load(paths)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.APIKey != "dk_auth_test" {
		t.Fatalf("expected api key saved, got %+v", cfg)
	}
}

func TestAuthRejectsInvalidKey(t *testing.T) {
	home := t.TempDir()
	_, err := runCLI(t, home, "auth", "bad")
	if err == nil {
		t.Fatal("expected auth error")
	}
}

func TestStartAndRestartRequireConfig(t *testing.T) {
	home := t.TempDir()

	_, err := runCLI(t, home, "start")
	if err == nil || !strings.Contains(err.Error(), "config not found") {
		t.Fatalf("expected start config error, got %v", err)
	}

	_, err = runCLI(t, home, "restart")
	if err == nil || !strings.Contains(err.Error(), "config not found") {
		t.Fatalf("expected restart config error, got %v", err)
	}
}

func TestStopNotRunningAndVersion(t *testing.T) {
	home := t.TempDir()

	out, err := runCLI(t, home, "stop")
	if err != nil {
		t.Fatalf("stop command error: %v", err)
	}
	if !strings.Contains(out, "is not running") {
		t.Fatalf("unexpected stop output: %q", out)
	}

	out, err = runCLI(t, home, "version")
	if err != nil {
		t.Fatalf("version command error: %v", err)
	}
	if !strings.Contains(out, "version=") {
		t.Fatalf("unexpected version output: %q", out)
	}
}

func TestLogsCommand(t *testing.T) {
	home := t.TempDir()
	paths := config.DefaultPaths(home)
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}

	out, err := runCLI(t, home, "logs", "-n", "2")
	if err != nil {
		t.Fatalf("logs command error: %v", err)
	}
	if !strings.Contains(out, "no log file found") {
		t.Fatalf("unexpected logs output: %q", out)
	}

	if err := os.WriteFile(paths.LogFile, []byte("a\nb\nc\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	out, err = runCLI(t, home, "logs", "-n", "2")
	if err != nil {
		t.Fatalf("logs command error: %v", err)
	}
	if out != "b\nc\n" {
		t.Fatalf("unexpected logs tail output: %q", out)
	}
}

func TestRunCommandRequiresConfig(t *testing.T) {
	home := t.TempDir()
	_, err := runCLI(t, home, "__run")
	if err == nil {
		t.Fatal("expected __run config load error")
	}
}

func TestUninstallRemovesRuntimeDir(t *testing.T) {
	home := t.TempDir()
	paths := config.DefaultPaths(home)
	if err := config.EnsureStateDirs(paths); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := os.WriteFile(paths.LogFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	out, err := runCLI(t, home, "uninstall")
	if err != nil {
		t.Fatalf("uninstall command error: %v", err)
	}
	if !strings.Contains(out, "uninstalled") {
		t.Fatalf("unexpected uninstall output: %q", out)
	}
	if _, err := os.Stat(paths.RootDir); !os.IsNotExist(err) {
		t.Fatalf("expected runtime dir removed, err=%v", err)
	}
}

func TestServiceCommands(t *testing.T) {
	home := t.TempDir()
	var installedHome string
	var removedHome string

	origInstall := installServiceFunc
	origRemove := removeServiceFunc
	t.Cleanup(func() {
		installServiceFunc = origInstall
		removeServiceFunc = origRemove
	})

	installServiceFunc = func(targetHome string) error {
		installedHome = targetHome
		return nil
	}
	removeServiceFunc = func(targetHome string) error {
		removedHome = targetHome
		return nil
	}

	out, err := runCLI(t, home, "service", "install")
	if err != nil {
		t.Fatalf("service install command error: %v", err)
	}
	if !strings.Contains(out, "service installed") {
		t.Fatalf("unexpected service install output: %q", out)
	}
	if installedHome != home {
		t.Fatalf("expected install to target home %q, got %q", home, installedHome)
	}

	out, err = runCLI(t, home, "service", "uninstall")
	if err != nil {
		t.Fatalf("service uninstall command error: %v", err)
	}
	if !strings.Contains(out, "service removed") {
		t.Fatalf("unexpected service uninstall output: %q", out)
	}
	if removedHome != home {
		t.Fatalf("expected uninstall to target home %q, got %q", home, removedHome)
	}
}

func TestServiceInstallCommandError(t *testing.T) {
	home := t.TempDir()
	origInstall := installServiceFunc
	t.Cleanup(func() { installServiceFunc = origInstall })

	installServiceFunc = func(string) error { return errors.New("boom") }
	_, err := runCLI(t, home, "service", "install")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected service install error, got %v", err)
	}
}

func TestRemoveSymlinkIfPresent(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	if err := removeSymlinkIfPresent(missing); err != nil {
		t.Fatalf("expected nil on missing path, got %v", err)
	}

	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatalf("write regular file: %v", err)
	}
	if err := removeSymlinkIfPresent(regular); err != nil {
		t.Fatalf("expected nil on regular file, got %v", err)
	}
	if _, err := os.Stat(regular); err != nil {
		t.Fatalf("regular file should remain: %v", err)
	}

	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("write target file: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if err := removeSymlinkIfPresent(link); err != nil {
		t.Fatalf("remove symlink: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("expected symlink removed, err=%v", err)
	}
}
