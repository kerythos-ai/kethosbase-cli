// Package config handles the two on-disk stores the CLI uses:
//
//   - the per-project link file (./kethosbase.json) — committable, no secrets;
//   - the user-global credentials file (~/.kethosbase/credentials.json, 0600) —
//     holds the access token and per-project database URLs.
//
// Keeping secrets out of the project file means the link file is safe to commit
// while the connection string stays in the user's home directory.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// ---- project link file (./kethosbase.json) ----

const ProjectFile = "kethosbase.json"

// Project is the committable link between a working directory and a Kethosbase
// project. It carries no secrets.
type Project struct {
	Ref           string `json:"ref"`
	APIURL        string `json:"api_url,omitempty"`
	MigrationsDir string `json:"migrations_dir,omitempty"`
	// SchemaDir holds the declared .sql schema files that `db diff` treats as the
	// source of truth (default: ./schema).
	SchemaDir string `json:"schema_dir,omitempty"`
}

// LoadProject reads ./kethosbase.json from dir. It returns (nil, nil) when the
// file is absent so callers can distinguish "not linked" from a read error.
func LoadProject(dir string) (*Project, error) {
	b, err := os.ReadFile(filepath.Join(dir, ProjectFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p Project
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// SaveProject writes ./kethosbase.json in dir (pretty-printed).
func SaveProject(dir string, p *Project) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ProjectFile), append(b, '\n'), 0o644)
}

// ---- global credentials (~/.kethosbase/credentials.json) ----

// Credentials is the user-global secret store.
type Credentials struct {
	AccessToken string                        `json:"access_token,omitempty"`
	Projects    map[string]ProjectCredentials `json:"projects,omitempty"`
}

// ProjectCredentials holds the secrets needed to operate on one project.
type ProjectCredentials struct {
	DBURL string `json:"db_url,omitempty"`
}

func credentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".kethosbase", "credentials.json"), nil
}

// LoadCredentials reads the global credentials, returning an empty (non-nil)
// store when the file does not exist yet.
func LoadCredentials() (*Credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Credentials{Projects: map[string]ProjectCredentials{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Projects == nil {
		c.Projects = map[string]ProjectCredentials{}
	}
	return &c, nil
}

// SaveCredentials writes the global credentials with 0600 perms (0700 dir).
func SaveCredentials(c *Credentials) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// DBURLFor returns the stored database URL for a project ref, if any.
func (c *Credentials) DBURLFor(ref string) string {
	if c == nil {
		return ""
	}
	return c.Projects[ref].DBURL
}
