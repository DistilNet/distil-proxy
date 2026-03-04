package cli

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/distilnet/distil-proxy/internal/config"
	"github.com/distilnet/distil-proxy/internal/daemon"
	"github.com/distilnet/distil-proxy/internal/version"
	"github.com/spf13/cobra"
)

func runCLI(t *testing.T, home string, args ...string) (string, error) {
	return runCLIWithInput(t, home, "", args...)
}

func runCLIWithInput(t *testing.T, home string, input string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("HOME", home)
	if _, ok := os.LookupEnv("DISTIL_TEST_REAL_POST_AUTH"); !ok {
		origPostAuthFinalize := postAuthFinalizeFunc
		postAuthFinalizeFunc = func(_ *cobra.Command, _ config.Paths, _ config.Config) error { return nil }
		t.Cleanup(func() { postAuthFinalizeFunc = origPostAuthFinalize })
	}

	cmd := NewRootCmd(version.DefaultInfo())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(args)

	err := cmd.Execute()
	return out.String(), err
}

type fakeAuthHTTPDoer struct {
	do func(*http.Request) (*http.Response, error)
}

func (f fakeAuthHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	return f.do(req)
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

func TestAuthCommandPerformsRestartAndVerificationFetch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DISTIL_TEST_REAL_POST_AUTH", "1")

	origRestart := postAuthRestartFunc
	origStatus := postAuthStatusFunc
	origSleep := postAuthSleepFunc
	origHTTPClient := postAuthHTTPClient
	t.Cleanup(func() {
		postAuthRestartFunc = origRestart
		postAuthStatusFunc = origStatus
		postAuthSleepFunc = origSleep
		postAuthHTTPClient = origHTTPClient
	})

	restartCalled := false
	postAuthRestartFunc = func(_ config.Paths, cfg config.Config) error {
		restartCalled = true
		if cfg.APIKey != "dk_auth_test" {
			t.Fatalf("expected auth key for restart, got %q", cfg.APIKey)
		}
		return nil
	}

	statusCalls := 0
	postAuthStatusFunc = func(_ config.Paths) (daemon.RuntimeStatus, error) {
		statusCalls++
		if statusCalls == 1 {
			return daemon.RuntimeStatus{WSState: "reconnecting"}, nil
		}
		return daemon.RuntimeStatus{WSState: "connected"}, nil
	}

	postAuthSleepFunc = func(time.Duration) {}

	var (
		gotRequestURL string
		gotAuthHeader string
	)
	postAuthHTTPClient = func(timeout time.Duration) authHTTPDoer {
		if timeout != postAuthFetchTimeout {
			t.Fatalf("expected timeout %s, got %s", postAuthFetchTimeout, timeout)
		}
		return fakeAuthHTTPDoer{
			do: func(req *http.Request) (*http.Response, error) {
				gotRequestURL = req.URL.String()
				gotAuthHeader = req.Header.Get("X-Distil-Key")
				return &http.Response{
					StatusCode: http.StatusOK,
					Header: http.Header{
						"X-Distil":        []string{"true"},
						"X-Distil-Source": []string{"proxy"},
					},
					Body: io.NopCloser(strings.NewReader("ok")),
				}, nil
			},
		}
	}

	out, err := runCLI(t, home, "auth", "dk_auth_test")
	if err != nil {
		t.Fatalf("auth command error: %v", err)
	}

	if !restartCalled {
		t.Fatal("expected auth to restart daemon")
	}
	if gotRequestURL != "https://proxy.distil.net/https://example.com" {
		t.Fatalf("unexpected verification fetch URL: %q", gotRequestURL)
	}
	if gotAuthHeader != "dk_auth_test" {
		t.Fatalf("expected verification fetch to use auth key, got %q", gotAuthHeader)
	}
	if !strings.Contains(out, "restarted daemon with updated auth") {
		t.Fatalf("expected restart output, got %q", out)
	}
	if !strings.Contains(out, "daemon websocket connected") {
		t.Fatalf("expected connected output, got %q", out)
	}
	if !strings.Contains(out, "verification fetch succeeded") {
		t.Fatalf("expected verification fetch output, got %q", out)
	}
}

func TestAuthCommandReturnsErrorWhenVerificationFetchFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DISTIL_TEST_REAL_POST_AUTH", "1")

	origRestart := postAuthRestartFunc
	origStatus := postAuthStatusFunc
	origHTTPClient := postAuthHTTPClient
	t.Cleanup(func() {
		postAuthRestartFunc = origRestart
		postAuthStatusFunc = origStatus
		postAuthHTTPClient = origHTTPClient
	})

	postAuthRestartFunc = func(_ config.Paths, _ config.Config) error { return nil }
	postAuthStatusFunc = func(_ config.Paths) (daemon.RuntimeStatus, error) {
		return daemon.RuntimeStatus{WSState: "connected"}, nil
	}
	postAuthHTTPClient = func(time.Duration) authHTTPDoer {
		return fakeAuthHTTPDoer{
			do: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusUnauthorized,
					Body:       io.NopCloser(strings.NewReader("invalid api key")),
				}, nil
			},
		}
	}

	out, err := runCLI(t, home, "auth", "dk_auth_test")
	if err == nil {
		t.Fatal("expected auth command error when verification fetch fails")
	}
	if !strings.Contains(err.Error(), "verification fetch failed with status 401") {
		t.Fatalf("unexpected auth command error: %v", err)
	}
	if !strings.Contains(out, "updated config") {
		t.Fatalf("expected config update output before failure, got %q", out)
	}

	cfg, loadErr := config.Load(config.DefaultPaths(home))
	if loadErr != nil {
		t.Fatalf("expected config written before verification failure: %v", loadErr)
	}
	if cfg.APIKey != "dk_auth_test" {
		t.Fatalf("expected api key persisted despite verification failure, got %+v", cfg)
	}
}

func TestAuthCommandInteractiveUsesVerificationFlow(t *testing.T) {
	home := t.TempDir()
	var keyEndpointCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/install/register":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"code_sent"}`))
		case "/api/v1/install/verify":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"verified","email":"cli@example.com","api_key":"dk_user_key","proxy_key":"dpk_daemon_key"}`))
		case "/api/v1/install/key":
			keyEndpointCalled = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"authenticated","email":"cli@example.com"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("DISTIL_AUTH_BASE_URL", server.URL)

	out, err := runCLIWithInput(t, home, "cli@example.com\n123456\n", "auth")
	if err != nil {
		t.Fatalf("interactive auth command error: %v", err)
	}
	if keyEndpointCalled {
		t.Fatal("expected key endpoint to be skipped when email is provided")
	}
	if !strings.Contains(out, "updated config") {
		t.Fatalf("unexpected output: %q", out)
	}

	paths := config.DefaultPaths(home)
	cfg, err := config.Load(paths)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.APIKey != "dk_user_key" {
		t.Fatalf("expected api key saved (preferred over proxy_key), got %+v", cfg)
	}
}

func TestAuthCommandInteractiveWithExistingAPIKeyStillRequiresCode(t *testing.T) {
	home := t.TempDir()
	var (
		keyEndpointCalled      bool
		registerEndpointCalled bool
		verifyEndpointCalled   bool
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/install/key":
			keyEndpointCalled = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"authenticated","email":"key-owner@example.com"}`))
		case "/api/v1/install/register":
			registerEndpointCalled = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"code_sent"}`))
		case "/api/v1/install/verify":
			verifyEndpointCalled = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"verified","email":"key-owner@example.com","api_key":"dk_user_key","proxy_key":"dpk_proxy_key"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("DISTIL_AUTH_BASE_URL", server.URL)

	_, err := runCLIWithInput(t, home, "dk_existing_key\n654321\n", "auth")
	if err != nil {
		t.Fatalf("interactive auth command error: %v", err)
	}
	if !keyEndpointCalled || !registerEndpointCalled || !verifyEndpointCalled {
		t.Fatalf("expected key/register/verify endpoints called: key=%t register=%t verify=%t", keyEndpointCalled, registerEndpointCalled, verifyEndpointCalled)
	}

	paths := config.DefaultPaths(home)
	cfg, err := config.Load(paths)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.APIKey != "dk_user_key" {
		t.Fatalf("expected api key saved (preferred over proxy_key), got %+v", cfg)
	}
}

func TestAuthRejectsInvalidKey(t *testing.T) {
	home := t.TempDir()
	_, err := runCLI(t, home, "auth", "bad")
	if err == nil {
		t.Fatal("expected auth error")
	}
}

func TestAuthRepairsInvalidExistingConfig(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{
			name:    "legacy-proxy-key",
			payload: `{"proxy_key":"dpk_legacy","server":"wss://proxy.distil.net/ws"}`,
		},
		{
			name:    "malformed-json",
			payload: `{"api_key":`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			paths := config.DefaultPaths(home)
			if err := config.EnsureStateDirs(paths); err != nil {
				t.Fatalf("ensure state dirs: %v", err)
			}
			if err := os.WriteFile(paths.ConfigFile, []byte(tc.payload), 0o600); err != nil {
				t.Fatalf("write config fixture: %v", err)
			}

			out, err := runCLI(t, home, "auth", "dk_auth_repair")
			if err != nil {
				t.Fatalf("auth command error: %v", err)
			}
			if !strings.Contains(out, "updated config") {
				t.Fatalf("unexpected output: %q", out)
			}

			cfg, err := config.Load(paths)
			if err != nil {
				t.Fatalf("load repaired config: %v", err)
			}
			if cfg.APIKey != "dk_auth_repair" {
				t.Fatalf("expected repaired api key, got %+v", cfg)
			}
		})
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

	if _, err := runCLI(t, home, "auth", "dk_service_install"); err != nil {
		t.Fatalf("seed config for service command: %v", err)
	}
	paths := config.DefaultPaths(home)
	serviceBinary := filepath.Join(paths.BinDir, "distil-proxy")
	if err := os.WriteFile(serviceBinary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("seed service binary: %v", err)
	}

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
	if _, err := runCLI(t, home, "auth", "dk_service_error"); err != nil {
		t.Fatalf("seed config for service install error path: %v", err)
	}
	paths := config.DefaultPaths(home)
	serviceBinary := filepath.Join(paths.BinDir, "distil-proxy")
	if err := os.WriteFile(serviceBinary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("seed service binary for error path: %v", err)
	}
	origInstall := installServiceFunc
	t.Cleanup(func() { installServiceFunc = origInstall })

	installServiceFunc = func(string) error { return errors.New("boom") }
	_, err := runCLI(t, home, "service", "install")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected service install error, got %v", err)
	}
}

func TestServiceInstallRequiresConfig(t *testing.T) {
	home := t.TempDir()
	_, err := runCLI(t, home, "service", "install")
	if err == nil || !strings.Contains(err.Error(), "config not found; run 'distil-proxy auth' first") {
		t.Fatalf("expected service install config error, got %v", err)
	}
}

func TestServiceInstallRequiresManagedBinary(t *testing.T) {
	home := t.TempDir()
	if _, err := runCLI(t, home, "auth", "dk_service_binary"); err != nil {
		t.Fatalf("seed config for managed binary check: %v", err)
	}

	_, err := runCLI(t, home, "service", "install")
	if err == nil || !strings.Contains(err.Error(), "service binary not found") {
		t.Fatalf("expected missing managed binary error, got %v", err)
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
