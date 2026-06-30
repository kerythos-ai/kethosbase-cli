// Package introspect reads a project's Postgres schema (tables, columns, enums)
// so the CLI can generate types from it.
package introspect

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Schema is the introspected shape of one Postgres schema.
type Schema struct {
	Name   string
	Tables []Table
	Enums  []Enum
}

type Table struct {
	Name    string
	Columns []Column
}

type Column struct {
	Name       string
	UDTName    string // pg udt_name, e.g. int4, text, _int4 (array), or an enum type name
	Nullable   bool
	HasDefault bool
	IsIdentity bool
}

type Enum struct {
	Name   string
	Labels []string
}

const columnsQuery = `
select c.table_name, c.column_name, c.udt_name,
       c.is_nullable = 'YES'      as nullable,
       c.column_default is not null as has_default,
       c.is_identity = 'YES'      as is_identity
from information_schema.columns c
join information_schema.tables t
  on t.table_schema = c.table_schema
 and t.table_name = c.table_name
 and t.table_type = 'BASE TABLE'
where c.table_schema = $1
order by c.table_name, c.ordinal_position`

const enumsQuery = `
select t.typname, e.enumlabel
from pg_type t
join pg_enum e on e.enumtypid = t.oid
join pg_namespace n on n.oid = t.typnamespace
where n.nspname = $1
order by t.typname, e.enumsortorder`

// Load reads tables, columns and enums for schema from the database at connURL.
func Load(ctx context.Context, connURL, schema string) (*Schema, error) {
	conn, err := pgx.Connect(ctx, connURL)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)

	out := &Schema{Name: schema}

	// Tables + columns, preserving order while grouping by table.
	rows, err := conn.Query(ctx, columnsQuery, schema)
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for rows.Next() {
		var tbl string
		var col Column
		if err := rows.Scan(&tbl, &col.Name, &col.UDTName, &col.Nullable, &col.HasDefault, &col.IsIdentity); err != nil {
			rows.Close()
			return nil, err
		}
		i, ok := idx[tbl]
		if !ok {
			i = len(out.Tables)
			idx[tbl] = i
			out.Tables = append(out.Tables, Table{Name: tbl})
		}
		out.Tables[i].Columns = append(out.Tables[i].Columns, col)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Enums.
	erows, err := conn.Query(ctx, enumsQuery, schema)
	if err != nil {
		return nil, err
	}
	eidx := map[string]int{}
	for erows.Next() {
		var name, label string
		if err := erows.Scan(&name, &label); err != nil {
			erows.Close()
			return nil, err
		}
		i, ok := eidx[name]
		if !ok {
			i = len(out.Enums)
			eidx[name] = i
			out.Enums = append(out.Enums, Enum{Name: name})
		}
		out.Enums[i].Labels = append(out.Enums[i].Labels, label)
	}
	erows.Close()
	if err := erows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}
