package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/distilnet/distil-proxy/internal/config"
	"github.com/distilnet/distil-proxy/internal/platform"
	"github.com/spf13/cobra"
)

var (
	installServiceFunc = platform.InstallServiceDefinition
	removeServiceFunc  = platform.RemoveServiceDefinitions
)

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage local daemon service registration",
	}

	cmd.AddCommand(newServiceInstallCmd())
	cmd.AddCommand(newServiceUninstallCmd())
	return cmd
}

func newServiceInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install and enable launchd/systemd user service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}
			if _, err := config.Load(paths); err != nil {
				if errors.Is(err, config.ErrConfigNotFound) {
					return errors.New("config not found; run 'distil-proxy auth' first")
				}
				return err
			}
			if err := config.EnsureStateDirs(paths); err != nil {
				return err
			}
			serviceBinary := filepath.Join(paths.BinDir, "distil-proxy")
			info, err := os.Stat(serviceBinary)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("service binary not found at %s", serviceBinary)
				}
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("service binary is not a regular file: %s", serviceBinary)
			}
			if info.Mode().Perm()&0o111 == 0 {
				return fmt.Errorf("service binary is not executable: %s", serviceBinary)
			}
			if err := installServiceFunc(paths.HomeDir); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "distil-proxy service installed")
			return nil
		},
	}
}

func newServiceUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove launchd/systemd user service definitions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}
			if err := removeServiceFunc(paths.HomeDir); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "distil-proxy service removed")
			return nil
		},
	}
}
