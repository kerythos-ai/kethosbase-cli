package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/kerythos-ai/kethosbase-cli/internal/api"
	"github.com/kerythos-ai/kethosbase-cli/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newLoginCmd() *cobra.Command {
	var email, password, apiURL string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with Kethosbase and store the session locally",
		Long: "Logs in to the Kethosbase control plane with your account email and\n" +
			"password, storing the returned session token in ~/.kethosbase/credentials.json.\n" +
			"The session has a limited lifetime; re-run `login` when it expires.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if email == "" {
				v, err := prompt("Email: ")
				if err != nil {
					return err
				}
				email = v
			}
			if password == "" {
				v, err := promptSecret("Password: ")
				if err != nil {
					return err
				}
				password = v
			}

			res, err := api.New(apiURL, "").Login(context.Background(), email, password)
			if err != nil {
				return err
			}

			creds, err := config.LoadCredentials()
			if err != nil {
				return err
			}
			creds.AccessToken = res.Token
			if err := config.SaveCredentials(creds); err != nil {
				return err
			}
			fmt.Printf("Logged in as %s (session valid until %s).\n",
				res.User.Email, res.ExpiresAt.Format("2006-01-02 15:04 MST"))
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "account email (prompted if omitted)")
	cmd.Flags().StringVar(&password, "password", "", "account password (prompted, hidden, if omitted)")
	cmd.Flags().StringVar(&apiURL, "api", "", "control-plane API base URL (default "+api.DefaultBaseURL+")")
	return cmd
}

func prompt(label string) (string, error) {
	fmt.Print(label)
	s, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(s), err
}

func promptSecret(label string) (string, error) {
	fmt.Print(label)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	return strings.TrimSpace(string(b)), err
}
