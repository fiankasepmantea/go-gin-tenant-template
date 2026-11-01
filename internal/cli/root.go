// Package cli holds the CLI commands (Cobra). The module generator and migrations follow in phase 2.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/version"
)

// NewRootCmd creates the root "tenantctl" command.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "tenantctl",
		Short:         "CLI for go-gin-tenant-template",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newVersionCmd(), newKeygenCmd(), newTenantCmd(), newUserCmd(), newAdminCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the build version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "version=%s commit=%s built=%s\n",
				version.Version, version.Commit, version.BuildDate)
		},
	}
}

func newKeygenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keygen",
		Short: "Generate a new PASETO v4 key for PASETO_KEY",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), auth.GenerateKeyHex())
		},
	}
}
