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
(hidden password prompt), `evanw/esbuild` (Go library — bundles Function TS/JS for
`functions deploy`; ADR-0091). External build tools: **Javy** (Bytecode Alliance,
Apache-2.0) is the JS→WASM compiler for `functions deploy` — a pinned binary
downloaded on first use with a SHA-256 check (`internal/functions/javytool`), plus
a vendored custom Javy plugin built from `/plugin`. Approved by ADR-0091 (Javy
lives in the CLI toolchain, not the product; ADR-0002's zero-Supabase rule is
about the product surface).

## Architecture

- `main.go` → `cli.Execute()`.
- `internal/cli/` — cobra commands: `root.go`, `login.go`, `link.go`, `migrate.go`,
  `gen.go`, `functions.go`.
- `internal/functions/` — the `functions deploy` pipeline: `bundle/` (esbuild +
  the `@kethosbase/functions` SDK shim), `javytool/` (pinned Javy download +
  vendored plugin + `javy build`), `wasmcheck/` (parses a module's imports/exports
  to validate imports ⊆ {kethosbase, wasi_snapshot_preview1} and `_start` export).
- `plugin/` — Rust source for the custom Javy plugin (WASI preview 1) that exposes
  the `__kb_*` bridge globals to QuickJS and forwards to the `kethosbase` host
  imports. Built once via `plugin/build.sh`; the initialized `.wasm` is vendored
  and `go:embed`-ed. See `plugin/README.md` for the shared bridge contract.
- `internal/api/` — thin HTTP client for the control-plane (management) API.
- `internal/config/` — two stores: `./kethosbase.json` (committable project link,
  no secrets) and `~/.kethosbase/credentials.json` (0600: session token +
  per-project DB connection strings).
- `internal/migrate/` — migration runner: discovers `*.sql` (lexical order),
  applies each in its own tx, records in a `kethosbase_migrations` ledger with a
  sha256 checksum; rejects an applied file that later changes (drift).
- `internal/introspect/` — reads tables/columns/enums from a schema (pgx).
- `internal/gen/` — renders an introspected schema to TypeScript (Supabase-shaped
  `Database`: per-table Row/Insert/Update + Enums + named enum unions).

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

## Distribution (Go single-binary + npm wrapper)

- `.goreleaser.yaml` builds raw binaries `kethosbase_<os>_<arch>[.exe]` (linux/
  darwin/windows × amd64/arm64) with the version stamped via ldflags into
  `internal/cli.version`.
- `.github/workflows/release.yml` runs GoReleaser on a `v*` tag → GitHub Release.
- `npm/` is the `@kethosbase/cli` wrapper: `install.js` (postinstall) downloads
  the matching `kethosbase_<os>_<arch>` asset from the release into `npm/bin/`;
  `bin/kethosbase.js` execs it. Ships no binary itself (tiny package).
- **Release flow:** `git tag v0.1.0 && git push --tags` (CI builds the release),
  then `cd npm && npm publish --access public`. Keep `npm/package.json` version
  == git tag. **Wrapper download is over HTTPS → release assets must be public.**
  If the repo/releases stay private, switch the wrapper to esbuild-style
  per-platform npm packages instead.

## TODO / roadmap

1. Publish the npm wrapper (owner has npm creds; this machine does not):
   `cd npm && npm publish --access public`. Repo is public; releases are public.
2. Live smoke: `migrate up` and `gen types` against a real project DB (GoTech
   once linked) — not yet run against live Postgres.
3. Platform ask: a personal access token (PAT) for non-interactive/CI auth, so
   the CLI is not limited to the ~7-day session token.
4. Later: `db diff`/`db push`, `migrate new <name>` scaffolder, views/functions
   in `gen types`.
