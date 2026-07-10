// Package schema powers the CLI's declarative-schema workflow: parse the
// desired schema from .sql files, compare it against the project's live schema
// (via internal/introspect), and emit the DDL that reconciles the difference.
//
// The model deliberately mirrors internal/introspect.Schema so the two sides of
// a diff are symmetric: tables, columns (name/type/nullability/default/identity)
// and enums. It carries no primary keys, foreign keys, indexes or constraints —
// introspection does not surface those today, so the diff cannot reason about
// them. See Diff for the full list of what is and isn't covered.
package schema

import (
	"sort"
	"strings"

	"github.com/kerythos-ai/kethosbase-cli/internal/introspect"
)

// Schema is a desired-or-current schema in the shape the diff engine compares.
type Schema struct {
	Name   string
	Tables []Table
	Enums  []Enum
}

// Table is one base table. Raw is the verbatim CREATE TABLE statement when the
// table came from a declared .sql file; it lets the diff emit a brand-new table
// faithfully instead of re-synthesising its DDL.
type Table struct {
	Name    string
	Columns []Column
	Raw     string
}

// Column is one column. UDT is normalised to a Postgres udt_name (e.g. int4,
// text, _int4 for an int[] array, or an enum type name) so it compares directly
// against introspect.Column.UDTName. Raw is the verbatim column definition from
// a declared file, used to emit a faithful ADD COLUMN.
type Column struct {
	Name       string
	UDT        string
	Nullable   bool
	HasDefault bool
	IsIdentity bool
	Raw        string
}

// Enum is a user-defined enum type and its labels, in declared order.
type Enum struct {
	Name   string
	Labels []string
}

func (t Table) column(name string) (Column, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return Column{}, false
}

func (s Schema) table(name string) (Table, bool) {
	for _, t := range s.Tables {
		if t.Name == name {
			return t, true
		}
	}
	return Table{}, false
}

func (s Schema) enum(name string) (Enum, bool) {
	for _, e := range s.Enums {
		if e.Name == name {
			return e, true
		}
	}
	return Enum{}, false
}

// RemoveTable drops a table from the schema by name, if present. It is used to
// exclude CLI-managed tables (e.g. the migration ledger) from a diff.
func (s *Schema) RemoveTable(name string) {
	for i, t := range s.Tables {
		if t.Name == name {
			s.Tables = append(s.Tables[:i], s.Tables[i+1:]...)
			return
		}
	}
}

// FromIntrospect adapts an introspected live schema into the diff model. The
// live side never has Raw DDL, which is fine: the diff only needs verbatim DDL
// for the desired side (what we are creating).
func FromIntrospect(s *introspect.Schema) *Schema {
	if s == nil {
		return &Schema{}
	}
	out := &Schema{Name: s.Name}
	for _, t := range s.Tables {
		nt := Table{Name: t.Name}
		for _, c := range t.Columns {
			nt.Columns = append(nt.Columns, Column{
				Name:       c.Name,
				UDT:        c.UDTName,
				Nullable:   c.Nullable,
				HasDefault: c.HasDefault,
				IsIdentity: c.IsIdentity,
			})
		}
		out.Tables = append(out.Tables, nt)
	}
	for _, e := range s.Enums {
		out.Enums = append(out.Enums, Enum{Name: e.Name, Labels: append([]string(nil), e.Labels...)})
	}
	return out
}

// typeAliases maps declared SQL type spellings to the Postgres udt_name that
// introspection reports, so `integer` compares equal to `int4`, etc. Length and
// precision (varchar(255), numeric(10,2)) are stripped before lookup — the diff
// does not track type modifiers (documented limitation).
var typeAliases = map[string]string{
	"int":                         "int4",
	"integer":                     "int4",
	"int4":                        "int4",
	"serial":                      "int4",
	"serial4":                     "int4",
	"smallint":                    "int2",
	"int2":                        "int2",
	"smallserial":                 "int2",
	"serial2":                     "int2",
	"bigint":                      "int8",
	"int8":                        "int8",
	"bigserial":                   "int8",
	"serial8":                     "int8",
	"bool":                        "bool",
	"boolean":                     "bool",
	"real":                        "float4",
	"float4":                      "float4",
	"double precision":            "float8",
	"float8":                      "float8",
	"numeric":                     "numeric",
	"decimal":                     "numeric",
	"money":                       "money",
	"text":                        "text",
	"varchar":                     "varchar",
	"character varying":           "varchar",
	"char":                        "bpchar",
	"character":                   "bpchar",
	"bpchar":                      "bpchar",
	"uuid":                        "uuid",
	"json":                        "json",
	"jsonb":                       "jsonb",
	"bytea":                       "bytea",
	"date":                        "date",
	"time":                        "time",
	"time without time zone":      "time",
	"timetz":                      "timetz",
	"time with time zone":         "timetz",
	"timestamp":                   "timestamp",
	"timestamp without time zone": "timestamp",
	"timestamptz":                 "timestamptz",
	"timestamp with time zone":    "timestamptz",
	"cidr":                        "cidr",
	"inet":                        "inet",
	"macaddr":                     "macaddr",
}

// serialTypes carry an implicit sequence default; a declared `serial` column is
// therefore treated as HasDefault so it doesn't look different from the int4
// identity/default column introspection reports.
var serialTypes = map[string]bool{
	"serial": true, "serial4": true, "smallserial": true,
	"serial2": true, "bigserial": true, "serial8": true,
}

// udtToSQL renders a udt_name back to a readable SQL type for synthesised DDL
// (ALTER … TYPE, and ADD COLUMN when no Raw is available). Arrays (leading "_")
// become `<elem>[]`. Unknown udts (e.g. enum type names) pass through verbatim.
func udtToSQL(udt string) string {
	if strings.HasPrefix(udt, "_") {
		return udtToSQL(udt[1:]) + "[]"
	}
	switch udt {
	case "int2":
		return "smallint"
	case "int4":
		return "integer"
	case "int8":
		return "bigint"
	case "bool":
		return "boolean"
	case "float4":
		return "real"
	case "float8":
		return "double precision"
	case "bpchar":
		return "char"
	case "timestamptz":
		return "timestamptz"
	case "timestamp":
		return "timestamp"
	case "timetz":
		return "timetz"
	default:
		// text, varchar, numeric, uuid, jsonb, date, enum type names, …
		return udt
	}
}

// sortedTableNames returns the union of table names across two schemas, sorted,
// so diff output is deterministic regardless of file or introspection order.
func sortedTableNames(a, b *Schema) []string {
	seen := map[string]bool{}
	for _, t := range a.Tables {
		seen[t.Name] = true
	}
	for _, t := range b.Tables {
		seen[t.Name] = true
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
