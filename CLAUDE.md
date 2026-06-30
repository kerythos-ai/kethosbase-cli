# kethosbase-cli — agent guidance

The official **Kethosbase** command-line tool (Go). Single static binary that
lets a developer authenticate, link a working directory to a project, and run
SQL migrations. In-house, built by Kerythos. Companion to the `@kethosbase/client`
JS SDK (at `C:\kerythos\kethosbase-js`) and the platform server (at
`C:\kerythos\kerythos_kethosbase`).

## Rules

- **English only** in code, comments, docs, commits.
- **Ask when in doubt** (scope, naming, new deps). This is a dev tool, so
  third-party deps are allowed (unlike the zero-dep SDK) — but still deliberate.
- Self-contained in this directory; do not modify the SDK or server from here.

## Commands

```bash
go build ./...        # compile
go test ./...         # unit tests (DB-free)
go vet ./...
go build -o kethosbase.exe .   # local binary
```

Go 1.26. Deps: `spf13/cobra` (commands), `jackc/pgx/v5` (Postgres), `golang.org/x/term`
(hidden password prompt).

## Architecture

- `main.go` → `cli.Execute()`.
- `internal/cli/` — cobra commands: `root.go`, `login.go`, `link.go`, `migrate.go`.
- `internal/api/` — thin HTTP client for the control-plane (management) API.
- `internal/config/` — two stores: `./kethosbase.json` (committable project link,
  no secrets) and `~/.kethosbase/credentials.json` (0600: session token +
  per-project DB connection strings).
- `internal/migrate/` — migration runner: discovers `*.sql` (lexical order),
  applies each in its own tx, records in a `kethosbase_migrations` ledger with a
  sha256 checksum; rejects an applied file that later changes (drift).

## Control-plane API contract (what the CLI targets)

Base `https://api.kethosbase.com` (override with `--api` / `KETHOSBASE_API_URL`).
Confirmed against the server source (`internal/api/*`):
- `POST /v1/auth/login` `{email,password}` → `{token:"kbses_…",expires_at,user}`.
  Bearer `kbses_` session token, ~7-day TTL. **No PAT/non-interactive token yet**
  (planned platform addition — would remove the TTL pain for CI).
- `GET /v1/orgs` → `{orgs:[{id,name}]}`; `GET /v1/orgs/{org}/projects` →
  `{projects:[{ref,name,status,environment,api_url}]}`. ref = `^[a-z][a-z0-9]{15}$`.
- `POST /v1/projects/{ref}/db-credentials` `{label,conn_limit}` →
  `{uri:"postgres://kbd_…:<pass>@db.kethosbase.com:5432/p{ref}?sslmode=verify-full",…}`.
  Password shown **once**. Gated by the project's `durable_sql_enabled` (operator
  flag, ADR-0039). Host is `db.<domain>` for shared placement, else the
  placement's `sql_endpoint`. DB name is `p{ref}`.

## State (2026-06-30)

- **v0 scaffolded, builds clean, unit tests green.** Commands: `login`, `link`,
  `migrate up`, `migrate status`. `migrate` is build-verified + unit-tested
  (Discover/checksum/order); **not yet run against a live Postgres** (Docker
  daemon was down locally) — first real smoke is `kethosbase migrate up` against
  the GoTech project once linked.
- Not yet published; no GitHub repo yet at time of writing this file.

## TODO / roadmap

1. Distribution: GoReleaser + GitHub Actions to build cross-platform binaries on
   tag, and the `@kethosbase/cli` **npm wrapper** that downloads the right binary
   (the chosen distribution: Go single-binary + npm wrapper).
2. Live smoke: `migrate up` against a real project DB.
3. `gen types` (introspect schema → TS types) — deferred from v0.
4. Platform ask: a personal access token (PAT) for non-interactive/CI auth, so
   the CLI is not limited to the 7-day session token.
5. Later: `db diff`/`db push`, `migrate new <name>` scaffolder.
