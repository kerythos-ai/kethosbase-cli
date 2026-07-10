package schema

import (
	"os"
	"path/filepath"
	"testing"
)

func findTable(s *Schema, name string) (Table, bool) { return s.table(name) }

func TestParseColumnsAndTypes(t *testing.T) {
	sql := `
-- users table
create table public.users (
  id          bigint generated always as identity primary key,
  email       text not null,
  nickname    varchar(50),
  age         integer,
  balance     numeric(10,2) not null default 0,
  is_active   boolean not null default true,
  created_at  timestamptz not null default now(),
  tags        text[],
  constraint users_email_key unique (email)
);`
	s, warns, err := Parse(sql, "public")
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	tbl, ok := findTable(s, "users")
	if !ok {
		t.Fatal("users table not parsed")
	}
	// The table-level constraint must not be counted as a column.
	if len(tbl.Columns) != 8 {
		t.Fatalf("expected 8 columns, got %d: %+v", len(tbl.Columns), tbl.Columns)
	}

	want := map[string]struct {
		udt        string
		nullable   bool
		hasDefault bool
		identity   bool
	}{
		"id":         {"int8", false, true, true},
		"email":      {"text", false, false, false},
		"nickname":   {"varchar", true, false, false},
		"age":        {"int4", true, false, false},
		"balance":    {"numeric", false, true, false},
		"is_active":  {"bool", false, true, false},
		"created_at": {"timestamptz", false, true, false},
		"tags":       {"_text", true, false, false},
	}
	for _, c := range tbl.Columns {
		w, ok := want[c.Name]
		if !ok {
			t.Errorf("unexpected column %q", c.Name)
			continue
		}
		if c.UDT != w.udt {
			t.Errorf("%s: udt = %q, want %q", c.Name, c.UDT, w.udt)
		}
		if c.Nullable != w.nullable {
			t.Errorf("%s: nullable = %v, want %v", c.Name, c.Nullable, w.nullable)
		}
		if c.HasDefault != w.hasDefault {
			t.Errorf("%s: hasDefault = %v, want %v", c.Name, c.HasDefault, w.hasDefault)
		}
		if c.IsIdentity != w.identity {
			t.Errorf("%s: identity = %v, want %v", c.Name, c.IsIdentity, w.identity)
		}
	}
}

func TestParseSerialImpliesDefault(t *testing.T) {
	s, _, err := Parse(`create table t (id serial, big bigserial);`, "public")
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := findTable(s, "t")
	if tbl.Columns[0].UDT != "int4" || !tbl.Columns[0].HasDefault {
		t.Errorf("serial: got %+v", tbl.Columns[0])
	}
	if tbl.Columns[1].UDT != "int8" || !tbl.Columns[1].HasDefault {
		t.Errorf("bigserial: got %+v", tbl.Columns[1])
	}
}

func TestParseMultiwordTypes(t *testing.T) {
	s, _, err := Parse(`create table t (
	  a double precision,
	  b timestamp without time zone,
	  c character varying(20),
	  d timestamp with time zone not null
	);`, "public")
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := findTable(s, "t")
	got := map[string]string{}
	for _, c := range tbl.Columns {
		got[c.Name] = c.UDT
	}
	for name, udt := range map[string]string{"a": "float8", "b": "timestamp", "c": "varchar", "d": "timestamptz"} {
		if got[name] != udt {
			t.Errorf("%s: udt = %q, want %q", name, got[name], udt)
		}
	}
}

func TestParseEnum(t *testing.T) {
	s, _, err := Parse(`create type public.mood as enum ('happy', 'sad', 'ok');`, "public")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Enums) != 1 {
		t.Fatalf("expected 1 enum, got %d", len(s.Enums))
	}
	e := s.Enums[0]
	if e.Name != "mood" {
		t.Errorf("enum name = %q", e.Name)
	}
	if len(e.Labels) != 3 || e.Labels[0] != "happy" || e.Labels[2] != "ok" {
		t.Errorf("labels = %v", e.Labels)
	}
}

func TestParseUnqualifyAndIfNotExists(t *testing.T) {
	s, _, err := Parse(`create table if not exists public.orders (id int);`, "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findTable(s, "orders"); !ok {
		t.Fatalf("orders not found; tables: %+v", s.Tables)
	}
}

func TestParseUnsupportedStatementsWarn(t *testing.T) {
	s, warns, err := Parse(`
create table keep (id int);
create index idx on keep (id);
create table asel as select 1;`, "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findTable(s, "keep"); !ok {
		t.Error("keep table should parse")
	}
	if len(warns) < 2 {
		t.Errorf("expected warnings for index + CREATE TABLE AS, got %v", warns)
	}
}

func TestParseDuplicateTableErrors(t *testing.T) {
	_, _, err := Parse(`create table a (id int); create table a (id int);`, "public")
	if err == nil {
		t.Fatal("expected duplicate-table error")
	}
}

func TestLoadDirOrdersFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "02_enums.sql", `create type status as enum ('a','b');`)
	writeFile(t, dir, "01_tables.sql", `create table t (id int, s status);`)
	writeFile(t, dir, "readme.txt", `ignored`)

	s, warns, err := LoadDir(dir, "public")
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
	if len(s.Tables) != 1 || len(s.Enums) != 1 {
		t.Fatalf("expected 1 table + 1 enum, got %d/%d", len(s.Tables), len(s.Enums))
	}
	// The enum-typed column should keep the enum type name as its udt.
	col := s.Tables[0].Columns[1]
	if col.Name != "s" || col.UDT != "status" {
		t.Errorf("enum column udt = %q (%q), want status", col.UDT, col.Name)
	}
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
