# Design: Declarative schemas (`kethosbase db diff`)

Status: implemented (MVP). Author: Kerythos. Date: 2026-07-10.

## Problem

The CLI ships an imperative migration workflow (`migrate up`/`status`): the
developer hand-writes ordered `*.sql` files and the runner applies them with a
checksum ledger. That is precise but tedious — the developer has to reason about
*how* to get from the current schema to the desired one and encode every step.

A declarative workflow inverts this: the developer keeps the **desired** schema
as the source of truth in `*.sql` files, and the tool figures out the migration
by comparing that declaration against the project's live schema.

## Approach

`kethosbase db diff`:

1. **Read the declaration.** Parse every `*.sql` file in the schema directory
   (`./schema` by default, or `schema_dir` in `kethosbase.json`) into an
   in-memory schema model (`internal/schema`).
2. **Introspect the live schema.** Reuse `internal/introspect` (the same code
   that powers `gen types`) to read the linked project's tables, columns and
   enums.
3. **Diff.** Compare desired vs current and produce an ordered list of changes
   (`internal/schema.Compute`).
4. **Emit.** Render the changes as a migration file body (`RenderMigration`).
   By default it prints to stdout (dry run); `--write` saves it as the next
   `NNNN_<slug>.sql` in the migrations directory; `--apply` saves and runs it
   through the existing migration runner (with a confirmation prompt).

The design deliberately **reuses the existing introspection and migration
machinery** rather than introducing a parallel path: the generated artifact is a
normal migration file, tracked by the same `kethosbase_migrations` ledger, so
declarative and hand-written migrations coexist.

### Why a hand-rolled DDL parser (and not a shadow database or a diff library)

Two alternatives were considered and rejected for the MVP:

- **Shadow database** (apply the declared SQL to a scratch Postgres, introspect
  it, diff two introspections). Robust, but requires the developer to provision
  and manage an empty throwaway database — heavy for an MVP, and untestable
  without a live Postgres.
- **A third-party schema-diff engine.** Adopting one is a dependency decision
  and out of scope without explicit approval (and most are Postgres-server-side
  or heavyweight).

Instead the MVP parses a **restricted subset of DDL** in-house
(`internal/schema/parse.go`) into the *same* model that introspection produces,
so the diff is a pure, dependency-free, unit-testable comparison of two
`schema.Schema` values. This keeps the whole flow testable without a database.

## What the diff covers

Only what introspection surfaces today, so the diff can reason about it:

| Object  | Detected                                                         |
|---------|------------------------------------------------------------------|
| Tables  | create (new), drop (removed — destructive, `--allow-drops` only) |
| Columns | add, drop (destructive), type change, `SET`/`DROP NOT NULL`      |
| Enums   | create (new), append label (`ALTER TYPE … ADD VALUE`)            |

- New tables and columns are emitted **verbatim** from the declared SQL, so the
  exact default expressions, inline constraints and formatting are preserved.
- Destructive changes (dropping a table or column) are **never** emitted by
  default — they surface as warnings. `--allow-drops` opts in.
- The CLI-managed migration ledger (`kethosbase_migrations`) is excluded from
  the comparison, so it is never reported as undeclared or dropped.

## Limitations (important — read before relying on it)

Introspection does not expose the following, so the diff **cannot** see them.
Migrations for these must still be hand-written:

- Primary keys, foreign keys, unique / check / exclusion constraints, indexes.
- Column **default expressions** — only *presence* of a default is known, so a
  default that changes is not diffed, and a synthesised `ADD COLUMN` (when no
  verbatim text is available) carries no default.
- Type **modifiers**: `varchar(255)`, `numeric(10,2)` — length/precision are
  stripped before comparison, so a modifier-only change is not detected.
- Column **renames** (seen as drop + add) and column reordering.
- Removing or reordering **enum labels** (Postgres cannot without recreating the
  type) — reported as a warning, left unchanged.
- Views, materialized views, functions, triggers, RLS policies, grants, and any
  schema other than the one being diffed (`--schema`, default `public`).

The parser is a **subset parser**, not a full SQL grammar. It understands
`CREATE TABLE [IF NOT EXISTS]` (with column definitions; table-level constraints
are skipped) and `CREATE TYPE … AS ENUM`. Any other statement (indexes,
`CREATE TABLE … AS SELECT`, functions, …) is skipped with a warning rather than
failing the parse.

Finally, the generated migration is a **starting point, not a guaranteed-safe
apply**: `ALTER … TYPE` and `SET NOT NULL` can rewrite or reject existing data.
The migration header repeats this. Review before applying.

## Files

- `internal/schema/schema.go`  — the shared schema model, type-alias table
  (`integer` ↔ `int4`, …), and `FromIntrospect` adapter.
- `internal/schema/parse.go`   — the restricted DDL parser (`LoadDir`, `Parse`).
- `internal/schema/diff.go`    — `Compute(desired, current, Options)` diff engine.
- `internal/schema/render.go`  — `RenderMigration` (diff → migration file body).
- `internal/cli/db.go`         — the `db diff` command.
- `internal/migrate/migrate.go` — `NextFilename` (next `NNNN_<slug>.sql`).

## Future work

- Extend `internal/introspect` to read primary keys, indexes and constraints,
  then widen the diff to cover them (the diff engine is structured to grow).
- Optional shadow-database mode for full-fidelity parsing of arbitrary DDL.
- `db diff --check` for CI (non-zero exit when the schema has drifted).
