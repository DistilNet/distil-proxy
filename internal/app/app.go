package app

import (
	"context"

	"github.com/exec-io/distil-proxy/internal/cli"
	"github.com/exec-io/distil-proxy/internal/version"
)

// Run wires the command tree and executes it with the provided context.
func Run(ctx context.Context, args []string) error {
	cmd := cli.NewRootCmd(version.DefaultInfo())
	cmd.SetArgs(args)

	return cmd.ExecuteContext(ctx)
}
