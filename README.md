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

# 5. Deploy an Edge Function written in TypeScript/JavaScript
kethosbase functions deploy src/hello.ts            # name defaults to "hello"
kethosbase functions deploy src/hello.ts --name greet --project abcdefghijklmnop
kethosbase functions deploy src/hello.ts --dry-run -o hello.wasm   # build only
```

### Functions (`functions deploy`)

`functions deploy <file.ts>` compiles a TypeScript/JavaScript function to a
WebAssembly module and uploads it via the management API. The pipeline is:

1. **Bundle** the entrypoint and its imports into a single JS file with
   [esbuild](https://esbuild.github.io/) (used as a Go library, no separate
   binary). The `@kethosbase/functions` SDK import resolves to the installed
   package if present in `node_modules`, otherwise to a built-in shim.
2. **Compile** the JS to `module.wasm` with [Javy](https://github.com/bytecodealliance/javy)
   (QuickJS-on-Wasm) plus a custom Kethosbase plugin that exposes the platform
   host functions (`db`, `fetch`, `secret`, `log`) to the JS runtime. Javy needs
   a small one-line wasm-opt patch to accept bulk-memory output (see `/plugin`).
   The right patched Javy for your platform is **downloaded automatically on
   first use** (checksum-verified, cached under `~/.kethosbase/tools`) — no Rust
   toolchain and no manual setup. Set `KETHOSBASE_JAVY` to a local patched `javy`
   to override, or `KETHOSBASE_JAVY_BASE_URL` for a mirror. The plugin is vendored
   (embedded).
3. **Validate** that the module imports only `kethosbase` + `wasi_snapshot_preview1`
   and exports `_start` (matching the platform's deploy-time validator), and is
   under 8 MiB.
4. **Upload** via `POST /v1/projects/{ref}/functions/{name}` with
   `Content-Type: application/wasm`, using the stored session token.

Realizes ADR-0091 (JS-on-WASM Functions). See `/plugin` for the Javy plugin and
the shared bridge contract with `@kethosbase/functions`.

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
