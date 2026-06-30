# kethosbase CLI

Command-line tool for the **Kethosbase** platform: log in, link a project, and
run database migrations from your terminal. Single static Go binary.

## Install

```bash
# Via npm (downloads the prebuilt binary for your platform)
npx @kethosbase/cli <command>
# or install it globally:
npm i -g @kethosbase/cli

# From source (Go 1.26+)
go install github.com/kerythos-ai/kethosbase-cli@latest
```

## Usage

```bash
# 1. Authenticate (stores a session token in ~/.kethosbase/credentials.json)
kethosbase login                      # prompts for email + password

# 2. Link this directory to a project (writes ./kethosbase.json, stores the DB URL)
kethosbase link                       # auto-selects if you own one project
kethosbase link --ref abcdefghijklmnop

# 3. Run migrations
kethosbase migrate up                 # apply all pending .sql files in ./migrations
kethosbase migrate status             # show applied vs pending
kethosbase migrate up --dir packages/db/migrations

# 4. Generate TypeScript types from the live schema
kethosbase gen types                  # prints to stdout
kethosbase gen types -o src/database.types.ts
kethosbase gen types --schema public --db-url "postgres://…"
```

## How it works

- **`login`** → `POST /v1/auth/login` on the control plane; stores the returned
  session token (`kbses_…`). Sessions have a limited lifetime — re-run `login`
  when it expires. (A non-interactive personal access token is a planned
  platform addition.)
- **`link`** → resolves your project via `/v1/orgs` + `/v1/orgs/{org}/projects`,
  writes the committable `kethosbase.json`, and mints a **durable SQL credential**
  (`kbd_…`) via `POST /v1/projects/{ref}/db-credentials`, storing the connection
  string in `~/.kethosbase/credentials.json` (0600). The project must have
  durable SQL enabled; otherwise pass `--db-url` with an existing connection
  string.
- **`migrate`** → connects to the project's Postgres and applies `*.sql` files in
  lexical order (e.g. `0001_init.sql`, `0002_…`), recording each in a
  `kethosbase_migrations` ledger table. Re-runs are idempotent; an applied file
  that changes on disk is rejected (checksum drift) — never edit an applied
  migration, add a new one.
- **`gen types`** → introspects the schema (tables, columns, enums) and emits a
  Supabase-shaped `Database` TypeScript type (per-table `Row`/`Insert`/`Update`
  plus an `Enums` map and named enum unions). Insert fields are optional when the
  database can supply them (default, identity, or nullable).

## Configuration

| What | Where | Secret? |
|------|-------|---------|
| Project link (`ref`, `api_url`, `migrations_dir`) | `./kethosbase.json` | no — commit it |
| Session token + per-project DB connection string | `~/.kethosbase/credentials.json` (0600) | yes — never commit |

`migrate` resolves the database URL in this order: `--db-url` flag →
`KETHOSBASE_DB_URL` env → the linked project's stored credential.

## Releasing

Distribution is **Go single binary + npm wrapper**:

1. Tag a version: `git tag v0.1.0 && git push origin v0.1.0`.
2. GitHub Actions ([.github/workflows/release.yml](.github/workflows/release.yml))
   runs GoReleaser, building cross-platform binaries and publishing them as a
   GitHub Release (`kethosbase_<os>_<arch>[.exe]` + `checksums.txt`).
3. Publish the npm wrapper (matching version) so `npx @kethosbase/cli` fetches
   those binaries:
   ```bash
   cd npm && npm publish --access public
   ```

> The wrapper downloads binaries from the release over HTTPS, so the release
> assets must be **publicly downloadable**. Keep `npm/package.json` `version` in
> lockstep with the git tag.

## License

MIT © Kerythos AI LLC. See [LICENSE](./LICENSE).
