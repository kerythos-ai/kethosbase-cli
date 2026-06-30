package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/kerythos-ai/kethosbase-cli/internal/api"
	"github.com/kerythos-ai/kethosbase-cli/internal/config"
	"github.com/spf13/cobra"
)

func newLinkCmd() *cobra.Command {
	var ref, apiURL, dbURL, migrationsDir string

	cmd := &cobra.Command{
		Use:   "link",
		Short: "Link this directory to a Kethosbase project",
		Long: "Writes ./kethosbase.json (the committable project link) and stores the\n" +
			"project's database connection string in ~/.kethosbase/credentials.json so\n" +
			"`migrate` can reach it. By default it mints a durable SQL credential (kbd_);\n" +
			"pass --db-url to store an existing connection string instead.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := context.Background()
			creds, err := config.LoadCredentials()
			if err != nil {
				return err
			}
			if creds.AccessToken == "" && dbURL == "" {
				return fmt.Errorf("not logged in: run `kethosbase login` first (or pass --db-url to link without the control plane)")
			}
			client := api.New(apiURL, creds.AccessToken)

			// Resolve the project (unless --db-url lets us skip the control plane
			// entirely, in which case --ref is required as the local identifier).
			var proj *api.Project
			if dbURL == "" || ref == "" {
				p, err := resolveProject(ctx, client, ref)
				if err != nil {
					return err
				}
				proj = p
				ref = p.Ref
			}

			// Obtain the database URL: an explicit one, or mint a durable credential.
			if dbURL == "" {
				cred, err := client.CreateDBCredential(ctx, ref, "kethosbase-cli", 20)
				if err != nil {
					return fmt.Errorf("mint database credential: %w\n(hint: the project needs durable SQL enabled, or pass --db-url with an existing connection string)", err)
				}
				dbURL = cred.URI
			}

			// Persist the committable link...
			link := &config.Project{Ref: ref, MigrationsDir: migrationsDir}
			if proj != nil {
				link.APIURL = proj.APIURL
			}
			if err := config.SaveProject(".", link); err != nil {
				return err
			}
			// ...and the secret connection string.
			creds.Projects[ref] = config.ProjectCredentials{DBURL: dbURL}
			if err := config.SaveCredentials(creds); err != nil {
				return err
			}

			fmt.Printf("Linked to project %s.\n", ref)
			fmt.Printf("  wrote %s and stored the DB connection in ~/.kethosbase/credentials.json\n", config.ProjectFile)
			fmt.Println("  next: kethosbase migrate up")
			return nil
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "project ref (selected automatically if you own exactly one)")
	cmd.Flags().StringVar(&apiURL, "api", "", "control-plane API base URL (default "+api.DefaultBaseURL+")")
	cmd.Flags().StringVar(&dbURL, "db-url", "", "use this Postgres connection string instead of minting one")
	cmd.Flags().StringVar(&migrationsDir, "dir", "migrations", "migrations directory recorded in kethosbase.json")
	return cmd
}

// resolveProject finds the target project: by --ref if given, else the sole
// project the account owns, else an error listing the choices.
func resolveProject(ctx context.Context, client *api.Client, ref string) (*api.Project, error) {
	orgs, err := client.ListOrgs(ctx)
	if err != nil {
		return nil, err
	}
	var all []api.Project
	for _, o := range orgs {
		ps, err := client.ListProjects(ctx, o.ID)
		if err != nil {
			return nil, err
		}
		all = append(all, ps...)
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("no projects found for this account")
	}
	if ref != "" {
		for i := range all {
			if all[i].Ref == ref {
				return &all[i], nil
			}
		}
		return nil, fmt.Errorf("project %q not found among your projects", ref)
	}
	if len(all) == 1 {
		return &all[0], nil
	}
	var names []string
	for _, p := range all {
		names = append(names, fmt.Sprintf("%s (%s)", p.Ref, p.Name))
	}
	return nil, fmt.Errorf("multiple projects — pass --ref <ref>:\n  %s", strings.Join(names, "\n  "))
}
