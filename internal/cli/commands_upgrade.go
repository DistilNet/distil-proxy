package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/distilnet/distil-proxy/internal/config"
	"github.com/distilnet/distil-proxy/internal/daemon"
	"github.com/distilnet/distil-proxy/internal/upgrade"
	"github.com/distilnet/distil-proxy/internal/version"
	"github.com/spf13/cobra"
)

type manualUpgradeManager interface {
	CheckAndUpgrade(ctx context.Context) (upgrade.CheckResult, error)
}

var (
	upgradeExecPathFunc = os.Executable
	upgradeManagerFunc  = func(cfg upgrade.ManagerConfig) manualUpgradeManager {
		return upgrade.NewManager(cfg)
	}
	postUpgradeHookFunc = runPostUpgradeHook
	upgradeRestartFunc  = daemon.Restart
	upgradeStatusFunc   = daemon.Status
	upgradeSleepFunc    = time.Sleep
	upgradeNowFunc      = time.Now
)

func newUpgradeCmd(info version.Info) *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade distil-proxy binary to the latest release",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}

			cfg, err := config.Load(paths)
			if err != nil {
				if errors.Is(err, config.ErrConfigNotFound) {
					return errors.New("config not found; run 'distil-proxy auth' first")
				}
				return err
			}
			cfg.ApplyDefaults()

			binaryPath, err := upgradeExecPathFunc()
			if err != nil {
				return fmt.Errorf("resolve executable path: %w", err)
			}

			currentVersion := normalizeUpgradeCurrentVersion(info.Version)
			result, err := upgradeManagerFunc(upgrade.ManagerConfig{
				Enabled:        true,
				APIKey:         strings.TrimSpace(cfg.APIKey),
				CurrentVersion: currentVersion,
				BinaryPath:     binaryPath,
				TempBinaryPath: binaryPath + ".new",
				BackupPath:     binaryPath + ".bak",
				StatePath:      paths.UpgradeStateFile,
			}).CheckAndUpgrade(cmd.Context())
			if err != nil {
				return fmt.Errorf("upgrade failed: %w", err)
			}

			if !result.Applied {
				latest := strings.TrimSpace(result.AvailableVersion)
				if latest != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "distil-proxy is already up to date (current %s, latest %s)\n", info.Version, latest)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "distil-proxy is already up to date (current %s)\n", info.Version)
				}
				return nil
			}

			targetVersion := strings.TrimSpace(result.AvailableVersion)
			if targetVersion == "" {
				targetVersion = "latest"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "upgraded distil-proxy binary to %s\n", targetVersion)

			if err := postUpgradeHookFunc(cmd, paths, cfg); err != nil {
				return err
			}

			fmt.Fprintln(cmd.OutOrStdout(), "upgrade complete")
			return nil
		},
	}
}

func normalizeUpgradeCurrentVersion(current string) string {
	normalized := strings.TrimSpace(strings.TrimPrefix(current, "v"))
	parts := strings.Split(normalized, ".")
	if normalized == "" || len(parts) == 0 || len(parts) > 3 {
		return "0.0.0"
	}
	for _, part := range parts {
		if part == "" {
			return "0.0.0"
		}
		if _, err := strconv.Atoi(part); err != nil {
			return "0.0.0"
		}
	}
	return normalized
}

func runPostUpgradeHook(cmd *cobra.Command, paths config.Paths, cfg config.Config) error {
	if err := upgradeRestartFunc(paths, cfg); err != nil {
		return fmt.Errorf("post-upgrade hook failed: restart daemon: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "post-upgrade hook: restarted daemon")

	if err := waitForDaemonConnectedAfterUpgrade(paths, postAuthConnectTimeout, postAuthConnectPollInterval); err != nil {
		return fmt.Errorf("post-upgrade hook failed: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "post-upgrade hook: daemon websocket connected")

	if !strings.HasPrefix(strings.TrimSpace(cfg.APIKey), "dk_") {
		fmt.Fprintln(cmd.OutOrStdout(), "post-upgrade hook: skipped verification fetch for proxy credential")
		return nil
	}

	source, err := runVerificationFetch(cfg.Server, cfg.APIKey)
	if err != nil {
		return fmt.Errorf("post-upgrade hook failed: %w", err)
	}
	if source != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "post-upgrade hook: verification fetch succeeded (source: %s)\n", source)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "post-upgrade hook: verification fetch succeeded")
	}

	return nil
}

func waitForDaemonConnectedAfterUpgrade(paths config.Paths, timeout, pollInterval time.Duration) error {
	deadline := upgradeNowFunc().Add(timeout)
	lastState := ""
	lastError := ""

	for {
		status, err := upgradeStatusFunc(paths)
		if err != nil {
			lastError = err.Error()
		} else {
			lastState = strings.TrimSpace(status.WSState)
			if lastState == "connected" {
				return nil
			}
			if msg := strings.TrimSpace(status.LastError); msg != "" {
				lastError = msg
			}
		}

		if !upgradeNowFunc().Before(deadline) {
			break
		}
		upgradeSleepFunc(pollInterval)
	}

	if lastError != "" {
		return fmt.Errorf("daemon did not reconnect within %s (last_state=%q, last_error=%s)", timeout, lastState, lastError)
	}
	return fmt.Errorf("daemon did not reconnect within %s (last_state=%q)", timeout, lastState)
}
