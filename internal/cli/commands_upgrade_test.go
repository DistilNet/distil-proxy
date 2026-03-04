package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/distilnet/distil-proxy/internal/config"
	"github.com/distilnet/distil-proxy/internal/daemon"
	"github.com/distilnet/distil-proxy/internal/upgrade"
	"github.com/spf13/cobra"
)

type fakeUpgradeManager struct {
	check func(context.Context) (upgrade.CheckResult, error)
}

func (f fakeUpgradeManager) CheckAndUpgrade(ctx context.Context) (upgrade.CheckResult, error) {
	if f.check == nil {
		return upgrade.CheckResult{}, nil
	}
	return f.check(ctx)
}

func TestUpgradeCommandRequiresConfig(t *testing.T) {
	home := t.TempDir()
	_, err := runCLI(t, home, "upgrade")
	if err == nil || !strings.Contains(err.Error(), "config not found; run 'distil-proxy auth' first") {
		t.Fatalf("expected upgrade config error, got %v", err)
	}
}

func TestUpgradeCommandNoopSkipsPostHook(t *testing.T) {
	home := t.TempDir()
	if _, err := runCLI(t, home, "auth", "dk_upgrade_noop"); err != nil {
		t.Fatalf("seed config for upgrade command: %v", err)
	}

	origManagerFunc := upgradeManagerFunc
	origExecPath := upgradeExecPathFunc
	origPostUpgradeHook := postUpgradeHookFunc
	t.Cleanup(func() {
		upgradeManagerFunc = origManagerFunc
		upgradeExecPathFunc = origExecPath
		postUpgradeHookFunc = origPostUpgradeHook
	})

	hookCalled := false
	upgradeManagerFunc = func(cfg upgrade.ManagerConfig) manualUpgradeManager {
		if cfg.CurrentVersion != "0.0.0" {
			t.Fatalf("expected normalized current version 0.0.0 for dev build, got %q", cfg.CurrentVersion)
		}
		if cfg.BinaryPath != "/tmp/distil-proxy" {
			t.Fatalf("expected binary path /tmp/distil-proxy, got %q", cfg.BinaryPath)
		}
		return fakeUpgradeManager{
			check: func(context.Context) (upgrade.CheckResult, error) {
				return upgrade.CheckResult{AvailableVersion: "1.6.2", Applied: false}, nil
			},
		}
	}
	upgradeExecPathFunc = func() (string, error) { return "/tmp/distil-proxy", nil }
	postUpgradeHookFunc = func(*cobra.Command, config.Paths, config.Config) error {
		hookCalled = true
		return nil
	}

	out, err := runCLI(t, home, "upgrade")
	if err != nil {
		t.Fatalf("upgrade command error: %v", err)
	}
	if !strings.Contains(out, "already up to date") {
		t.Fatalf("expected up-to-date output, got %q", out)
	}
	if hookCalled {
		t.Fatal("expected post-upgrade hook not to run on no-op upgrade")
	}
}

func TestUpgradeCommandAppliesUpgradeAndRunsHook(t *testing.T) {
	home := t.TempDir()
	if _, err := runCLI(t, home, "auth", "dk_upgrade_apply"); err != nil {
		t.Fatalf("seed config for upgrade command: %v", err)
	}

	origManagerFunc := upgradeManagerFunc
	origExecPath := upgradeExecPathFunc
	origPostUpgradeHook := postUpgradeHookFunc
	t.Cleanup(func() {
		upgradeManagerFunc = origManagerFunc
		upgradeExecPathFunc = origExecPath
		postUpgradeHookFunc = origPostUpgradeHook
	})

	hookCalled := false
	upgradeManagerFunc = func(cfg upgrade.ManagerConfig) manualUpgradeManager {
		return fakeUpgradeManager{
			check: func(context.Context) (upgrade.CheckResult, error) {
				return upgrade.CheckResult{AvailableVersion: "1.6.2", Applied: true}, nil
			},
		}
	}
	upgradeExecPathFunc = func() (string, error) { return "/tmp/distil-proxy", nil }
	postUpgradeHookFunc = func(_ *cobra.Command, _ config.Paths, cfg config.Config) error {
		hookCalled = true
		if cfg.APIKey != "dk_upgrade_apply" {
			t.Fatalf("expected saved api key passed to post-upgrade hook, got %q", cfg.APIKey)
		}
		return nil
	}

	out, err := runCLI(t, home, "upgrade")
	if err != nil {
		t.Fatalf("upgrade command error: %v", err)
	}
	if !strings.Contains(out, "upgraded distil-proxy binary to 1.6.2") {
		t.Fatalf("expected applied upgrade output, got %q", out)
	}
	if !strings.Contains(out, "upgrade complete") {
		t.Fatalf("expected completion output, got %q", out)
	}
	if !hookCalled {
		t.Fatal("expected post-upgrade hook to run after applying upgrade")
	}
}

func TestUpgradeCommandPostHookRestartAndReconnectForProxyCredential(t *testing.T) {
	home := t.TempDir()
	if _, err := runCLI(t, home, "auth", "dpk_upgrade_hook"); err != nil {
		t.Fatalf("seed config for upgrade command: %v", err)
	}

	origManagerFunc := upgradeManagerFunc
	origExecPath := upgradeExecPathFunc
	origPostUpgradeHook := postUpgradeHookFunc
	origRestart := upgradeRestartFunc
	origStatus := upgradeStatusFunc
	origSleep := upgradeSleepFunc
	origNow := upgradeNowFunc
	t.Cleanup(func() {
		upgradeManagerFunc = origManagerFunc
		upgradeExecPathFunc = origExecPath
		postUpgradeHookFunc = origPostUpgradeHook
		upgradeRestartFunc = origRestart
		upgradeStatusFunc = origStatus
		upgradeSleepFunc = origSleep
		upgradeNowFunc = origNow
	})

	upgradeManagerFunc = func(cfg upgrade.ManagerConfig) manualUpgradeManager {
		return fakeUpgradeManager{
			check: func(context.Context) (upgrade.CheckResult, error) {
				return upgrade.CheckResult{AvailableVersion: "1.6.2", Applied: true}, nil
			},
		}
	}
	upgradeExecPathFunc = func() (string, error) { return "/tmp/distil-proxy", nil }
	postUpgradeHookFunc = runPostUpgradeHook

	restartCalled := false
	upgradeRestartFunc = func(_ config.Paths, cfg config.Config) error {
		restartCalled = true
		if cfg.APIKey != "dpk_upgrade_hook" {
			t.Fatalf("expected proxy credential key for restart, got %q", cfg.APIKey)
		}
		return nil
	}
	statusCalls := 0
	upgradeStatusFunc = func(_ config.Paths) (daemon.RuntimeStatus, error) {
		statusCalls++
		if statusCalls == 1 {
			return daemon.RuntimeStatus{WSState: "reconnecting"}, nil
		}
		return daemon.RuntimeStatus{WSState: "connected"}, nil
	}
	upgradeSleepFunc = func(time.Duration) {}
	upgradeNowFunc = time.Now

	out, err := runCLI(t, home, "upgrade")
	if err != nil {
		t.Fatalf("upgrade command error: %v", err)
	}
	if !restartCalled {
		t.Fatal("expected post-upgrade hook to restart daemon")
	}
	if !strings.Contains(out, "post-upgrade hook: restarted daemon") {
		t.Fatalf("expected restart output, got %q", out)
	}
	if !strings.Contains(out, "post-upgrade hook: daemon websocket connected") {
		t.Fatalf("expected reconnect output, got %q", out)
	}
	if !strings.Contains(out, "post-upgrade hook: skipped verification fetch for proxy credential") {
		t.Fatalf("expected dpk verification skip output, got %q", out)
	}
}

func TestUpgradeCommandPostHookVerificationFetchForAPIKey(t *testing.T) {
	home := t.TempDir()
	if _, err := runCLI(t, home, "auth", "dk_upgrade_hook"); err != nil {
		t.Fatalf("seed config for upgrade command: %v", err)
	}

	origManagerFunc := upgradeManagerFunc
	origExecPath := upgradeExecPathFunc
	origPostUpgradeHook := postUpgradeHookFunc
	origRestart := upgradeRestartFunc
	origStatus := upgradeStatusFunc
	origHTTPClient := postAuthHTTPClient
	t.Cleanup(func() {
		upgradeManagerFunc = origManagerFunc
		upgradeExecPathFunc = origExecPath
		postUpgradeHookFunc = origPostUpgradeHook
		upgradeRestartFunc = origRestart
		upgradeStatusFunc = origStatus
		postAuthHTTPClient = origHTTPClient
	})

	upgradeManagerFunc = func(cfg upgrade.ManagerConfig) manualUpgradeManager {
		return fakeUpgradeManager{
			check: func(context.Context) (upgrade.CheckResult, error) {
				return upgrade.CheckResult{AvailableVersion: "1.6.2", Applied: true}, nil
			},
		}
	}
	upgradeExecPathFunc = func() (string, error) { return "/tmp/distil-proxy", nil }
	postUpgradeHookFunc = runPostUpgradeHook
	upgradeRestartFunc = func(config.Paths, config.Config) error { return nil }
	upgradeStatusFunc = func(config.Paths) (daemon.RuntimeStatus, error) {
		return daemon.RuntimeStatus{WSState: "connected"}, nil
	}
	postAuthHTTPClient = func(time.Duration) authHTTPDoer {
		return fakeAuthHTTPDoer{
			do: func(_ *http.Request) (*http.Response, error) {
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

	out, err := runCLI(t, home, "upgrade")
	if err != nil {
		t.Fatalf("upgrade command error: %v", err)
	}
	if !strings.Contains(out, "post-upgrade hook: verification fetch succeeded (source: proxy)") {
		t.Fatalf("expected verification fetch output, got %q", out)
	}
}

func TestUpgradeCommandPostHookRestartFailure(t *testing.T) {
	home := t.TempDir()
	if _, err := runCLI(t, home, "auth", "dpk_upgrade_fail"); err != nil {
		t.Fatalf("seed config for upgrade command: %v", err)
	}

	origManagerFunc := upgradeManagerFunc
	origExecPath := upgradeExecPathFunc
	origPostUpgradeHook := postUpgradeHookFunc
	origRestart := upgradeRestartFunc
	t.Cleanup(func() {
		upgradeManagerFunc = origManagerFunc
		upgradeExecPathFunc = origExecPath
		postUpgradeHookFunc = origPostUpgradeHook
		upgradeRestartFunc = origRestart
	})

	upgradeManagerFunc = func(cfg upgrade.ManagerConfig) manualUpgradeManager {
		return fakeUpgradeManager{
			check: func(context.Context) (upgrade.CheckResult, error) {
				return upgrade.CheckResult{AvailableVersion: "1.6.2", Applied: true}, nil
			},
		}
	}
	upgradeExecPathFunc = func() (string, error) { return "/tmp/distil-proxy", nil }
	postUpgradeHookFunc = runPostUpgradeHook
	upgradeRestartFunc = func(config.Paths, config.Config) error { return errors.New("boom") }

	_, err := runCLI(t, home, "upgrade")
	if err == nil {
		t.Fatal("expected post-upgrade hook failure when restart fails")
	}
	if !strings.Contains(err.Error(), "post-upgrade hook failed: restart daemon: boom") {
		t.Fatalf("unexpected upgrade command error: %v", err)
	}
}
