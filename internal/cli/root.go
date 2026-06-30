package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is overridden at build time via -ldflags "-X .../cli.version=...".
var version = "0.1.0-dev"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "kethosbase",
		Short:         "Kethosbase CLI — manage your project from the terminal",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newLoginCmd())
	root.AddCommand(newLinkCmd())
	root.AddCommand(newMigrateCmd())
	root.AddCommand(newGenCmd())
	return root
}

// Execute runs the root command and maps any error to a non-zero exit.
func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
