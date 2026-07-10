package schema

import (
	"fmt"
	"sort"
	"strings"
)

// ChangeKind categorises a single diff step.
type ChangeKind string

const (
	CreateEnum      ChangeKind = "create enum"
	AddEnumValue    ChangeKind = "add enum value"
	CreateTable     ChangeKind = "create table"
	DropTable       ChangeKind = "drop table"
	AddColumn       ChangeKind = "add column"
	DropColumn      ChangeKind = "drop column"
	AlterColumnType ChangeKind = "alter column type"
	SetNotNull      ChangeKind = "set not null"
	DropNotNull     ChangeKind = "drop not null"
)

// Change is one reconciling step: a human summary plus the SQL that applies it.
// Destructive changes (dropping tables/columns) are only emitted when the caller
// opts in; otherwise they are surfaced as warnings so nothing is silently lost.
type Change struct {
	Kind        ChangeKind
	Object      string // "table" or "table.column"
	SQL         string
	Destructive bool
}

// Diff is the result of comparing a desired schema against the current one.
type Diff struct {
	Changes  []Change
	Warnings []string
}

// Empty reports whether there is nothing to apply.
func (d *Diff) Empty() bool { return len(d.Changes) == 0 }

// Options tunes what the diff is willing to emit.
type Options struct {
	// AllowDrops includes destructive DROP TABLE / DROP COLUMN statements. When
	// false (the default) such differences become warnings instead.
	AllowDrops bool
}

// Compute diffs a desired schema (parsed from declared .sql files) against the
// current live schema (from introspection) and returns the ordered changes that
// reconcile current → desired.
//
// COVERED (what introspection surfaces, so what the diff can reason about):
//   - tables:  CREATE TABLE for new tables; DROP TABLE for removed ones (only
//     with Options.AllowDrops, else a warning).
//   - columns: ADD COLUMN, DROP COLUMN (drop is destructive/opt-in), ALTER TYPE
//     when the udt_name differs, and SET/DROP NOT NULL for nullability changes.
//   - enums:   CREATE TYPE … AS ENUM for new enums; ALTER TYPE … ADD VALUE for
//     labels appended to an existing enum.
//
// NOT COVERED (introspection does not expose these today — treat migrations for
// them as hand-written):
//   - primary keys, foreign keys, unique/check/exclusion constraints, indexes.
//   - column DEFAULT *expressions* (only presence is known, so defaults are
//     never altered), and type modifiers such as varchar(255) / numeric(10,2).
//   - column renames (seen as a drop + add), column reordering.
//   - removing or reordering enum labels (Postgres cannot without a rebuild).
//   - views, materialized views, functions, triggers, RLS policies, grants,
//     schemas other than the one being diffed.
//
// The output is a starting point for a migration, not a guaranteed-safe apply:
// ALTER … TYPE and SET NOT NULL can fail or rewrite data. Review before applying.
func Compute(desired, current *Schema, opts Options) *Diff {
	d := &Diff{}
	schemaName := desired.Name
	if schemaName == "" {
		schemaName = current.Name
	}

	d.diffEnums(desired, current)
	d.diffTables(desired, current, schemaName, opts)
	return d
}

func (d *Diff) diffEnums(desired, current *Schema) {
	names := map[string]bool{}
	for _, e := range desired.Enums {
		names[e.Name] = true
	}
	for _, e := range current.Enums {
		names[e.Name] = true
	}
	ordered := make([]string, 0, len(names))
	for n := range names {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)

	for _, name := range ordered {
		want, inWant := desired.enum(name)
		have, inHave := current.enum(name)
		switch {
		case inWant && !inHave:
			d.add(Change{
				Kind:   CreateEnum,
				Object: name,
				SQL:    fmt.Sprintf("create type %s as enum (%s);", qualIdent(name), enumLabelList(want.Labels)),
			})
		case inWant && inHave:
			// Only appended labels are expressible via ALTER TYPE ADD VALUE.
			existing := map[string]bool{}
			for _, l := range have.Labels {
				existing[l] = true
			}
			for _, l := range want.Labels {
				if !existing[l] {
					d.add(Change{
						Kind:   AddEnumValue,
						Object: name,
						SQL:    fmt.Sprintf("alter type %s add value if not exists %s;", qualIdent(name), sqlLiteral(l)),
					})
				}
			}
			wantSet := map[string]bool{}
			for _, l := range want.Labels {
				wantSet[l] = true
			}
			for _, l := range have.Labels {
				if !wantSet[l] {
					d.warn(fmt.Sprintf("enum %q: label %q exists in the database but not in the declared schema — Postgres cannot drop an enum label without recreating the type; left unchanged", name, l))
				}
			}
		case !inWant && inHave:
			d.warn(fmt.Sprintf("enum %q exists in the database but is not declared — not dropped (a type may be in use); remove it by hand if intended", name))
		}
	}
}

func (d *Diff) diffTables(desired, current *Schema, schemaName string, opts Options) {
	for _, name := range sortedTableNames(desired, current) {
		want, inWant := desired.table(name)
		have, inHave := current.table(name)
		switch {
		case inWant && !inHave:
			d.add(Change{
				Kind:   CreateTable,
				Object: name,
				SQL:    createTableSQL(want, schemaName),
			})
		case !inWant && inHave:
			if opts.AllowDrops {
				d.add(Change{
					Kind:        DropTable,
					Object:      name,
					SQL:         fmt.Sprintf("drop table %s;", qualTable(schemaName, name)),
					Destructive: true,
				})
			} else {
				d.warn(fmt.Sprintf("table %q exists in the database but is not declared — not dropped (pass --allow-drops to include a DROP TABLE)", name))
			}
		case inWant && inHave:
			d.diffColumns(want, have, schemaName, opts)
		}
	}
}

func (d *Diff) diffColumns(want, have Table, schemaName string, opts Options) {
	tbl := qualTable(schemaName, want.Name)

	// Added / changed columns, in declared order.
	for _, wc := range want.Columns {
		hc, ok := have.column(wc.Name)
		if !ok {
			d.add(Change{
				Kind:   AddColumn,
				Object: want.Name + "." + wc.Name,
				SQL:    fmt.Sprintf("alter table %s add column %s;", tbl, columnDDL(wc)),
			})
			continue
		}
		if wc.UDT != hc.UDT {
			d.add(Change{
				Kind:   AlterColumnType,
				Object: want.Name + "." + wc.Name,
				SQL: fmt.Sprintf("alter table %s alter column %s type %s using %s::%s;",
					tbl, qualIdent(wc.Name), udtToSQL(wc.UDT), qualIdent(wc.Name), udtToSQL(wc.UDT)),
			})
		}
		if wc.Nullable != hc.Nullable {
			if wc.Nullable {
				d.add(Change{
					Kind:   DropNotNull,
					Object: want.Name + "." + wc.Name,
					SQL:    fmt.Sprintf("alter table %s alter column %s drop not null;", tbl, qualIdent(wc.Name)),
				})
			} else {
				d.add(Change{
					Kind:   SetNotNull,
					Object: want.Name + "." + wc.Name,
					SQL:    fmt.Sprintf("alter table %s alter column %s set not null;", tbl, qualIdent(wc.Name)),
				})
			}
		}
	}

	// Removed columns.
	for _, hc := range have.Columns {
		if _, ok := want.column(hc.Name); ok {
			continue
		}
		if opts.AllowDrops {
			d.add(Change{
				Kind:        DropColumn,
				Object:      want.Name + "." + hc.Name,
				SQL:         fmt.Sprintf("alter table %s drop column %s;", tbl, qualIdent(hc.Name)),
				Destructive: true,
			})
		} else {
			d.warn(fmt.Sprintf("column %q.%q exists in the database but is not declared — not dropped (pass --allow-drops to include a DROP COLUMN)", have.Name, hc.Name))
		}
	}
}

func (d *Diff) add(c Change)  { d.Changes = append(d.Changes, c) }
func (d *Diff) warn(s string) { d.Warnings = append(d.Warnings, s) }

// createTableSQL prefers the verbatim declared statement (so defaults,
// constraints and formatting are preserved exactly); it falls back to a
// synthesised CREATE TABLE only when no raw text is available.
func createTableSQL(t Table, schemaName string) string {
	if strings.TrimSpace(t.Raw) != "" {
		return ensureSemicolon(t.Raw)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "create table %s (\n", qualTable(schemaName, t.Name))
	for i, c := range t.Columns {
		b.WriteString("  ")
		b.WriteString(columnDDL(c))
		if i < len(t.Columns)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString(");")
	return b.String()
}

// columnDDL renders a column definition. It uses the declared Raw text when
// present (preserving the exact default expression) and otherwise synthesises
// `name type [not null]` — note a synthesised column carries no default because
// introspection does not expose the default expression.
func columnDDL(c Column) string {
	if strings.TrimSpace(c.Raw) != "" {
		return collapseSpaces(c.Raw)
	}
	s := qualIdent(c.Name) + " " + udtToSQL(c.UDT)
	if !c.Nullable {
		s += " not null"
	}
	return s
}

func enumLabelList(labels []string) string {
	parts := make([]string, len(labels))
	for i, l := range labels {
		parts[i] = sqlLiteral(l)
	}
	return strings.Join(parts, ", ")
}

func sqlLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func qualTable(schemaName, table string) string {
	if schemaName == "" || schemaName == "public" {
		return qualIdent(table)
	}
	return qualIdent(schemaName) + "." + qualIdent(table)
}

// qualIdent quotes an identifier only when it isn't a plain lowercase token, to
// keep the common case readable while staying safe for mixed-case/reserved names.
func qualIdent(name string) string {
	plain := name != ""
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			plain = false
			break
		}
	}
	if plain {
		return name
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func ensureSemicolon(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), ";")
	return s + ";"
}
