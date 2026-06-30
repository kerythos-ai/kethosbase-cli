package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/kerythos-ai/kethosbase-cli/internal/config"
	"github.com/kerythos-ai/kethosbase-cli/internal/migrate"
	"github.com/spf13/cobra"
)

func newMigrateCmd() *cobra.Command {
	var dir, dbURL string

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply SQL migrations to the linked project's database",
	}
	cmd.PersistentFlags().StringVar(&dir, "dir", "",
		"migrations directory (default: migrations_dir from kethosbase.json, else ./migrations)")
	cmd.PersistentFlags().StringVar(&dbURL, "db-url", "",
		"Postgres connection string (overrides the linked project and KETHOSBASE_DB_URL)")

	up := &cobra.Command{
		Use:   "up",
		Short: "Apply all pending migrations",
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, mdir, err := resolve(dbURL, dir)
			if err != nil {
				return err
			}
			applied, err := migrate.Up(context.Background(), conn, mdir)
			if err != nil {
				return err
			}
			if len(applied) == 0 {
				fmt.Println("Already up to date.")
				return nil
			}
			fmt.Printf("Applied %d migration(s):\n", len(applied))
			for _, v := range applied {
				fmt.Printf("  ✓ %s\n", v)
			}
			return nil
		},
	}

	status := &cobra.Command{
		Use:   "status",
		Short: "Show applied and pending migrations",
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, mdir, err := resolve(dbURL, dir)
			if err != nil {
				return err
			}
			st, err := migrate.GetStatus(context.Background(), conn, mdir)
			if err != nil {
				return err
			}
			for _, v := range st.Applied {
				fmt.Printf("  ✓ %s\n", v)
			}
			for _, v := range st.Pending {
				fmt.Printf("  • %s (pending)\n", v)
			}
			fmt.Printf("\n%d applied, %d pending\n", len(st.Applied), len(st.Pending))
			return nil
		},
	}

	cmd.AddCommand(up, status)
	return cmd
}

// resolve picks the database URL and migrations directory from, in order of
// precedence: explicit flag, the KETHOSBASE_DB_URL env var, then the linked
// project (./kethosbase.json + the stored credential). The directory falls back
// to the project's migrations_dir, then ./migrations.
func resolve(dbURLFlag, dirFlag string) (dbURL, dir string, err error) {
	proj, err := config.LoadProject(".")
	if err != nil {
		return "", "", err
	}

	dbURL = dbURLFlag
	if dbURL == "" {
		dbURL = os.Getenv("KETHOSBASE_DB_URL")
	}
	if dbURL == "" && proj != nil {
		creds, err := config.LoadCredentials()
		if err != nil {
			return "", "", err
		}
		dbURL = creds.DBURLFor(proj.Ref)
	}
	if dbURL == "" {
		return "", "", fmt.Errorf("no database URL: pass --db-url, set KETHOSBASE_DB_URL, or run `kethosbase link` first")
	}

	dir = dirFlag
	if dir == "" && proj != nil {
		dir = proj.MigrationsDir
	}
	if dir == "" {
		dir = "migrations"
	}
	return dbURL, dir, nil
}
