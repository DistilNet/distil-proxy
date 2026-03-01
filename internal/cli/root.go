package cli

import (
	"fmt"

	"github.com/exec-io/distil-proxy/internal/version"
	"github.com/spf13/cobra"
)

// NewRootCmd constructs the CLI root for distil-proxy.
func NewRootCmd(info version.Info) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "distil-proxy",
		Short:         "Distil proxy daemon client",
		Long:          "distil-proxy is the public daemon client for Distil proxy fetching.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Version = info.Version
	cmd.AddCommand(newStartCmd())
	cmd.AddCommand(newStopCmd())
	cmd.AddCommand(newRestartCmd())
	cmd.AddCommand(newStatusCmd())
	cmd.AddCommand(newAuthCmd())
	cmd.AddCommand(newLogsCmd())
	cmd.AddCommand(newUninstallCmd())
	cmd.AddCommand(newRunCmd())
	cmd.AddCommand(newVersionCmd(info))

	return cmd
}

func newVersionCmd(info version.Info) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build version information",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), info.String())
		},
	}
}
