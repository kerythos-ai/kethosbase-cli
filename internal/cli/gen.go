package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/kerythos-ai/kethosbase-cli/internal/gen"
	"github.com/kerythos-ai/kethosbase-cli/internal/introspect"
	"github.com/spf13/cobra"
)

func newGenCmd() *cobra.Command {
	var dbURL, schema, output string

	cmd := &cobra.Command{
		Use:   "gen",
		Short: "Generate code from the linked project's database",
	}
	cmd.PersistentFlags().StringVar(&dbURL, "db-url", "",
		"Postgres connection string (overrides the linked project and KETHOSBASE_DB_URL)")
	cmd.PersistentFlags().StringVar(&schema, "schema", "public", "database schema to introspect")
	cmd.PersistentFlags().StringVarP(&output, "output", "o", "", "write to a file instead of stdout")

	types := &cobra.Command{
		Use:   "types",
		Short: "Generate TypeScript types from the database schema",
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, _, err := resolve(dbURL, "")
			if err != nil {
				return err
			}
			s, err := introspect.Load(context.Background(), conn, schema)
			if err != nil {
				return err
			}
			out := gen.TypeScript(s)
			if output == "" {
				fmt.Print(out)
				return nil
			}
			if err := os.WriteFile(output, []byte(out), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Wrote %s (%d tables, %d enums).\n", output, len(s.Tables), len(s.Enums))
			return nil
		},
	}

	cmd.AddCommand(types)
	return cmd
}
