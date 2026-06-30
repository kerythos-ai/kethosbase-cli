// Package api is a thin client for the Kethosbase control-plane (management) API
// at https://api.kethosbase.com — distinct from a project's data API. It backs
// the `login` and `link` commands.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultBaseURL = "https://api.kethosbase.com"

// Client talks to the control plane with an optional session token (kbses_).
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// ---- response shapes (only the fields the CLI uses) ----

type LoginResult struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
}

type Org struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Project struct {
	Ref         string `json:"ref"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Environment string `json:"environment"`
	APIURL      string `json:"api_url"`
}

type DBCredential struct {
	ID       string `json:"id"`
	User     string `json:"user"`
	Password string `json:"password"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	URI      string `json:"uri"`
}

// ---- calls ----

func (c *Client) Login(ctx context.Context, email, password string) (*LoginResult, error) {
	var out LoginResult
	err := c.do(ctx, http.MethodPost, "/v1/auth/login",
		map[string]string{"email": email, "password": password}, &out)
	return &out, err
}

func (c *Client) ListOrgs(ctx context.Context) ([]Org, error) {
	var out struct {
		Orgs []Org `json:"orgs"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/orgs", nil, &out)
	return out.Orgs, err
}

func (c *Client) ListProjects(ctx context.Context, orgID string) ([]Project, error) {
	var out struct {
		Projects []Project `json:"projects"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/orgs/"+orgID+"/projects", nil, &out)
	return out.Projects, err
}

// CreateDBCredential mints a durable SQL credential (kbd_) for a project. The
// password is returned only here, embedded in URI; store it now or it is gone.
func (c *Client) CreateDBCredential(ctx context.Context, ref, label string, connLimit int) (*DBCredential, error) {
	var out DBCredential
	err := c.do(ctx, http.MethodPost, "/v1/projects/"+ref+"/db-credentials",
		map[string]any{"label": label, "conn_limit": connLimit}, &out)
	return &out, err
}

// do performs a JSON request and decodes the response, normalizing both the
// PostgREST-style `{message}` and the control envelope `{error:{message}}`.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s", method, path, apiError(data, resp.StatusCode))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func apiError(body []byte, status int) string {
	var e struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	switch {
	case e.Error.Message != "":
		return e.Error.Message
	case e.Message != "":
		return e.Message
	default:
		return fmt.Sprintf("request failed (%d)", status)
	}
}
