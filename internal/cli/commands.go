package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/exec-io/distil-proxy/internal/config"
	"github.com/exec-io/distil-proxy/internal/daemon"
	"github.com/exec-io/distil-proxy/internal/observability"
	"github.com/exec-io/distil-proxy/internal/platform"
	"github.com/spf13/cobra"
)

func newStartCmd() *cobra.Command {
	var foreground bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start daemon in the background",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}

			cfg, err := config.Load(paths)
			if err != nil {
				if errors.Is(err, config.ErrConfigNotFound) {
					return errors.New("config not found; run 'distil-proxy auth <dk_key>' first")
				}
				return err
			}

			if foreground {
				return daemon.StartForeground(cmd.Context(), paths, cfg, cmd.OutOrStdout())
			}

			if err := daemon.Start(paths, cfg); err != nil {
				return err
			}

			fmt.Fprintln(cmd.OutOrStdout(), "distil-proxy started")
			return nil
		},
	}

	cmd.Flags().BoolVar(&foreground, "foreground", false, "run daemon in foreground mode")
	return cmd
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}

			err = daemon.Stop(paths)
			if err != nil {
				if errors.Is(err, daemon.ErrNotRunning) {
					fmt.Fprintln(cmd.OutOrStdout(), "distil-proxy is not running")
					return nil
				}
				return err
			}

			fmt.Fprintln(cmd.OutOrStdout(), "distil-proxy stopped")
			return nil
		},
	}
}

func newRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}

			cfg, err := config.Load(paths)
			if err != nil {
				if errors.Is(err, config.ErrConfigNotFound) {
					return errors.New("config not found; run 'distil-proxy auth <dk_key>' first")
				}
				return err
			}

			if err := daemon.Restart(paths, cfg); err != nil {
				return err
			}

			fmt.Fprintln(cmd.OutOrStdout(), "distil-proxy restarted")
			return nil
		},
	}
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}

			status, err := daemon.Status(paths)
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "running: %t\n", status.Running)
			fmt.Fprintf(cmd.OutOrStdout(), "pid: %d\n", status.PID)
			fmt.Fprintf(cmd.OutOrStdout(), "ws_state: %s\n", status.WSState)
			fmt.Fprintf(cmd.OutOrStdout(), "uptime_seconds: %d\n", status.UptimeSeconds)
			fmt.Fprintf(cmd.OutOrStdout(), "jobs_served: %d\n", status.JobsServed)
			fmt.Fprintf(cmd.OutOrStdout(), "connect_attempts: %d\n", status.ConnectAttempts)
			fmt.Fprintf(cmd.OutOrStdout(), "reconnects: %d\n", status.Reconnects)
			fmt.Fprintf(cmd.OutOrStdout(), "jobs_success: %d\n", status.JobsSuccess)
			fmt.Fprintf(cmd.OutOrStdout(), "jobs_error: %d\n", status.JobsError)
			fmt.Fprintf(cmd.OutOrStdout(), "avg_latency_ms: %d\n", status.AvgLatencyMS)
			fmt.Fprintf(cmd.OutOrStdout(), "latency_le_100_ms: %d\n", status.LatencyLE100MS)
			fmt.Fprintf(cmd.OutOrStdout(), "latency_le_500_ms: %d\n", status.LatencyLE500MS)
			fmt.Fprintf(cmd.OutOrStdout(), "latency_le_1000_ms: %d\n", status.LatencyLE1000MS)
			fmt.Fprintf(cmd.OutOrStdout(), "latency_gt_1000_ms: %d\n", status.LatencyGT1000MS)

			if !status.LastHeartbeat.IsZero() {
				fmt.Fprintf(cmd.OutOrStdout(), "last_heartbeat: %s\n", status.LastHeartbeat.Format("2006-01-02T15:04:05Z07:00"))
			}
			if status.LastError != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "last_error: %s\n", status.LastError)
			}

			return nil
		},
	}
}

func newAuthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "auth <dk_key>",
		Short: "Update API key in local config",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			apiKey := strings.TrimSpace(args[0])
			if err := config.ValidateAPIKey(apiKey); err != nil {
				return err
			}

			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}

			cfg, err := config.Load(paths)
			if err != nil {
				// Allow auth to recover malformed/legacy configs by rewriting from defaults.
				cfg = config.Config{}
			}

			cfg.APIKey = apiKey
			if err := config.Save(paths, cfg); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "updated config: %s\n", paths.ConfigFile)
			return nil
		},
	}
}

func newLogsCmd() *cobra.Command {
	var lineCount int

	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Print recent daemon logs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}

			lines, err := daemon.ReadLogTail(paths, lineCount)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					fmt.Fprintln(cmd.OutOrStdout(), "no log file found")
					return nil
				}
				return err
			}

			for _, line := range lines {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			return nil
		},
	}

	cmd.Flags().IntVarP(&lineCount, "lines", "n", 100, "number of lines to print")

	return cmd
}

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "__run",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}

			cfg, err := config.Load(paths)
			if err != nil {
				return err
			}

			logger, err := observability.NewLogger(cfg.LogLevel, os.Stdout)
			if err != nil {
				return err
			}
			logger.LogAttrs(cmd.Context(), slog.LevelInfo, "daemon runtime boot")

			return daemon.Run(cmd.Context(), paths, cfg, logger)
		},
	}
}

func newUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove daemon files and service definitions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}

			if err := daemon.Stop(paths); err != nil && !errors.Is(err, daemon.ErrNotRunning) {
				return err
			}

			if err := platform.RemoveServiceDefinitions(paths.HomeDir); err != nil {
				return err
			}

			for _, link := range []string{"/usr/local/bin/distil-proxy", filepath.Join(paths.HomeDir, ".local", "bin", "distil-proxy")} {
				if removeErr := removeSymlinkIfPresent(link); removeErr != nil {
					return removeErr
				}
			}

			if err := os.RemoveAll(paths.RootDir); err != nil {
				return fmt.Errorf("remove runtime directory: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), "distil-proxy uninstalled")
			return nil
		},
	}
}

func removeSymlinkIfPresent(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lstat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove symlink %s: %w", path, err)
	}
	return nil
}
