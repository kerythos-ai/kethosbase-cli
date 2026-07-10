package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kerythos-ai/kethosbase-cli/internal/config"
	"github.com/kerythos-ai/kethosbase-cli/internal/introspect"
	"github.com/kerythos-ai/kethosbase-cli/internal/migrate"
	"github.com/kerythos-ai/kethosbase-cli/internal/schema"
	"github.com/spf13/cobra"
)

func newDBCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Work with the linked project's database schema",
	}
	cmd.AddCommand(newDBDiffCmd())
	return cmd
}

func newDBDiffCmd() *cobra.Command {
	var (
		dbURL, schemaDir, pgSchema, migDir, outFile, name string
		allowDrops, write, apply, yes                     bool
	)

	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Diff the declared .sql schema against the live database and emit a migration",
		Long: "Reads the declared schema (the .sql files in ./schema, the source of truth),\n" +
			"introspects the linked project's live schema, computes the difference, and emits\n" +
			"a migration that reconciles the database to match the declaration.\n\n" +
			"By default the migration is printed to stdout (a dry run). Use --write to save it\n" +
			"as the next migration file, or --apply to save and run it.\n\n" +
			"Coverage is a subset of DDL: tables, columns (type/nullability), and enums. It does\n" +
			"NOT diff primary keys, foreign keys, indexes, constraints, default expressions,\n" +
			"renames, views, functions, triggers, RLS, or grants — see the migration header.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, mdir, err := resolve(dbURL, migDir)
			if err != nil {
				return err
			}

			sdir, err := resolveSchemaDir(schemaDir)
			if err != nil {
				return err
			}

			desired, warnings, err := schema.LoadDir(sdir, pgSchema)
			if err != nil {
				return err
			}
			for _, w := range warnings {
				fmt.Fprintf(os.Stderr, "parse warning: %s\n", w)
			}

			live, err := introspect.Load(context.Background(), conn, pgSchema)
			if err != nil {
				return err
			}

			current := schema.FromIntrospect(live)
			// The migration ledger is a CLI-managed table, not part of the user's
			// declared schema; drop it from the comparison so it is never reported
			// as an undeclared table (which --allow-drops would otherwise DROP).
			current.RemoveTable(migrate.LedgerTable)

			diff := schema.Compute(desired, current, schema.Options{AllowDrops: allowDrops})
			for _, w := range diff.Warnings {
				fmt.Fprintf(os.Stderr, "warning: %s\n", w)
			}

			body := schema.RenderMigration(diff)

			if diff.Empty() {
				fmt.Fprintln(os.Stderr, "No changes: the declared schema matches the database.")
				if !write && !apply && outFile == "" {
					return nil
				}
			}

			// Dry run (default): print the migration to stdout.
			if !write && !apply && outFile == "" {
				fmt.Print(body)
				summarise(diff)
				return nil
			}

			// Determine the output path.
			path := outFile
			if path == "" {
				slug := name
				if slug == "" {
					slug = "declared_schema_diff"
				}
				path = filepath.Join(mdir, migrate.NextFilename(mdir, slug))
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Wrote %s\n", path)
			summarise(diff)

			if !apply {
				return nil
			}
			if diff.Empty() {
				return nil
			}

			if !yes {
				ans, err := prompt(fmt.Sprintf("Apply %d change(s) to the database now? [y/N]: ", len(diff.Changes)))
				if err != nil {
					return err
				}
				if !isYes(ans) {
					fmt.Fprintln(os.Stderr, "Aborted; the migration file was kept but not applied.")
					return nil
				}
			}
			applied, err := migrate.Up(context.Background(), conn, mdir)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Applied %d migration(s).\n", len(applied))
			return nil
		},
	}

	cmd.Flags().StringVar(&dbURL, "db-url", "",
		"Postgres connection string (overrides the linked project and KETHOSBASE_DB_URL)")
	cmd.Flags().StringVar(&schemaDir, "schema-dir", "",
		"directory of declared .sql files (default: schema_dir from kethosbase.json, else ./schema)")
	cmd.Flags().StringVar(&pgSchema, "schema", "public", "database schema to diff")
	cmd.Flags().StringVar(&migDir, "dir", "",
		"migrations directory for the output file (default: migrations_dir, else ./migrations)")
	cmd.Flags().StringVarP(&outFile, "out", "o", "", "write the migration to this exact path instead of the migrations dir")
	cmd.Flags().StringVar(&name, "name", "", "slug for the generated migration file name")
	cmd.Flags().BoolVar(&allowDrops, "allow-drops", false, "include destructive DROP TABLE / DROP COLUMN statements")
	cmd.Flags().BoolVar(&write, "write", false, "save the migration as the next file in the migrations dir")
	cmd.Flags().BoolVar(&apply, "apply", false, "save the migration and run it (implies --write; prompts unless --yes)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the apply confirmation prompt")
	return cmd
}

// resolveSchemaDir picks the declared-schema directory: explicit flag → the
// linked project's schema_dir → ./schema.
func resolveSchemaDir(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	proj, err := config.LoadProject(".")
	if err != nil {
		return "", err
	}
	if proj != nil && proj.SchemaDir != "" {
		return proj.SchemaDir, nil
	}
	return "schema", nil
}

func summarise(d *schema.Diff) {
	if d.Empty() {
		return
	}
	destructive := 0
	for _, c := range d.Changes {
		if c.Destructive {
			destructive++
		}
	}
	if destructive > 0 {
		fmt.Fprintf(os.Stderr, "%d change(s), %d destructive.\n", len(d.Changes), destructive)
	} else {
		fmt.Fprintf(os.Stderr, "%d change(s).\n", len(d.Changes))
	}
}

func isYes(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes"
}
