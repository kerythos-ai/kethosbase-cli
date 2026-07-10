package schema

import (
	"strings"
	"testing"
)

// diffSQL parses declared SQL, builds a current schema from the given tables/
// enums, and returns the computed diff.
func diffOf(t *testing.T, declared string, current *Schema, opts Options) *Diff {
	t.Helper()
	desired, warns, err := Parse(declared, "public")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("parse warnings: %v", warns)
	}
	if current == nil {
		current = &Schema{Name: "public"}
	}
	return Compute(desired, current, opts)
}

func hasKind(d *Diff, k ChangeKind) *Change {
	for i := range d.Changes {
		if d.Changes[i].Kind == k {
			return &d.Changes[i]
		}
	}
	return nil
}

func TestDiffCreateTable(t *testing.T) {
	d := diffOf(t, `create table users (id bigint, email text not null);`, nil, Options{})
	c := hasKind(d, CreateTable)
	if c == nil {
		t.Fatalf("expected a create table change, got %+v", d.Changes)
	}
	if c.Object != "users" {
		t.Errorf("object = %q", c.Object)
	}
	// New tables emit the declared statement verbatim.
	if !strings.Contains(c.SQL, "create table users") || !strings.Contains(c.SQL, "email text not null") {
		t.Errorf("unexpected create SQL: %s", c.SQL)
	}
}

func TestDiffDropTableGuarded(t *testing.T) {
	current := &Schema{Name: "public", Tables: []Table{{Name: "old"}}}

	// Without AllowDrops: a warning, no change.
	d := diffOf(t, `create table keep (id int);`, current, Options{})
	if hasKind(d, DropTable) != nil {
		t.Error("drop table should be suppressed without --allow-drops")
	}
	if len(d.Warnings) == 0 {
		t.Error("expected a warning about the undeclared table")
	}

	// With AllowDrops: a destructive DROP TABLE.
	d = diffOf(t, `create table keep (id int);`, current, Options{AllowDrops: true})
	c := hasKind(d, DropTable)
	if c == nil || !c.Destructive {
		t.Fatalf("expected destructive drop table, got %+v", d.Changes)
	}
	if !strings.Contains(c.SQL, "drop table old") {
		t.Errorf("drop SQL = %s", c.SQL)
	}
}

func TestDiffAddAndDropColumn(t *testing.T) {
	current := &Schema{Name: "public", Tables: []Table{{
		Name: "users",
		Columns: []Column{
			{Name: "id", UDT: "int8", Nullable: false},
			{Name: "legacy", UDT: "text", Nullable: true},
		},
	}}}
	declared := `create table users (id bigint not null, email text not null);`

	// Default: add email, warn about legacy (not dropped).
	d := diffOf(t, declared, current, Options{})
	add := hasKind(d, AddColumn)
	if add == nil || add.Object != "users.email" {
		t.Fatalf("expected add column users.email, got %+v", d.Changes)
	}
	if !strings.Contains(add.SQL, "add column") || !strings.Contains(add.SQL, "email") {
		t.Errorf("add SQL = %s", add.SQL)
	}
	if hasKind(d, DropColumn) != nil {
		t.Error("drop column should be suppressed by default")
	}
	if len(d.Warnings) == 0 {
		t.Error("expected warning about undeclared column legacy")
	}

	// With drops: legacy is dropped.
	d = diffOf(t, declared, current, Options{AllowDrops: true})
	drop := hasKind(d, DropColumn)
	if drop == nil || !drop.Destructive || drop.Object != "users.legacy" {
		t.Fatalf("expected destructive drop of users.legacy, got %+v", d.Changes)
	}
}

func TestDiffAlterColumnType(t *testing.T) {
	current := &Schema{Name: "public", Tables: []Table{{
		Name:    "t",
		Columns: []Column{{Name: "n", UDT: "int4", Nullable: true}},
	}}}
	d := diffOf(t, `create table t (n bigint);`, current, Options{})
	c := hasKind(d, AlterColumnType)
	if c == nil {
		t.Fatalf("expected alter column type, got %+v", d.Changes)
	}
	if !strings.Contains(c.SQL, "type bigint") || !strings.Contains(c.SQL, "using n::bigint") {
		t.Errorf("alter SQL = %s", c.SQL)
	}
}

func TestDiffNullabilityBothDirections(t *testing.T) {
	current := &Schema{Name: "public", Tables: []Table{{
		Name: "t",
		Columns: []Column{
			{Name: "a", UDT: "text", Nullable: true},  // will become NOT NULL
			{Name: "b", UDT: "text", Nullable: false}, // will become nullable
		},
	}}}
	d := diffOf(t, `create table t (a text not null, b text);`, current, Options{})
	if c := hasKind(d, SetNotNull); c == nil || c.Object != "t.a" {
		t.Errorf("expected set not null on t.a, got %+v", d.Changes)
	}
	if c := hasKind(d, DropNotNull); c == nil || c.Object != "t.b" {
		t.Errorf("expected drop not null on t.b, got %+v", d.Changes)
	}
}

func TestDiffCreateEnum(t *testing.T) {
	d := diffOf(t, `create type mood as enum ('happy','sad');`, nil, Options{})
	c := hasKind(d, CreateEnum)
	if c == nil {
		t.Fatalf("expected create enum, got %+v", d.Changes)
	}
	if !strings.Contains(c.SQL, "create type mood as enum ('happy', 'sad')") {
		t.Errorf("enum SQL = %s", c.SQL)
	}
}

func TestDiffEnumAddValueAndRemovedWarn(t *testing.T) {
	current := &Schema{Name: "public", Enums: []Enum{
		{Name: "mood", Labels: []string{"happy", "sad", "legacy"}},
	}}
	// Declared adds "great", drops "legacy".
	d := diffOf(t, `create type mood as enum ('happy','sad','great');`, current, Options{})
	c := hasKind(d, AddEnumValue)
	if c == nil {
		t.Fatalf("expected add enum value, got %+v", d.Changes)
	}
	if !strings.Contains(c.SQL, "add value if not exists 'great'") {
		t.Errorf("add value SQL = %s", c.SQL)
	}
	// Removed label cannot be applied → warning.
	foundWarn := false
	for _, w := range d.Warnings {
		if strings.Contains(w, "legacy") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected a warning about dropped label legacy, got %v", d.Warnings)
	}
}

func TestDiffEmptyWhenIdentical(t *testing.T) {
	current := &Schema{Name: "public", Tables: []Table{{
		Name: "t",
		Columns: []Column{
			{Name: "id", UDT: "int8", Nullable: false, HasDefault: true, IsIdentity: true},
			{Name: "name", UDT: "text", Nullable: true},
		},
	}}}
	d := diffOf(t, `create table t (id bigint generated always as identity, name text);`, current, Options{})
	if !d.Empty() {
		t.Fatalf("expected empty diff, got %+v", d.Changes)
	}
	body := RenderMigration(d)
	if !strings.Contains(body, "No changes") {
		t.Errorf("render should note no changes: %s", body)
	}
}

func TestRemoveTableExcludesFromDiff(t *testing.T) {
	current := &Schema{Name: "public", Tables: []Table{
		{Name: "kethosbase_migrations"},
		{Name: "keep", Columns: []Column{{Name: "id", UDT: "int4", Nullable: true}}},
	}}
	current.RemoveTable("kethosbase_migrations")
	if len(current.Tables) != 1 || current.Tables[0].Name != "keep" {
		t.Fatalf("RemoveTable left %+v", current.Tables)
	}
	// With the ledger removed, an --allow-drops diff must not try to drop it.
	d := diffOf(t, `create table keep (id int);`, current, Options{AllowDrops: true})
	if !d.Empty() {
		t.Fatalf("expected empty diff after removing ledger, got %+v", d.Changes)
	}
}

func TestRenderMigrationIncludesWarningsAndDestructiveMarker(t *testing.T) {
	current := &Schema{Name: "public", Tables: []Table{{Name: "old"}}}
	d := diffOf(t, `create table keep (id int);`, current, Options{AllowDrops: true})
	body := RenderMigration(d)
	if !strings.Contains(body, "[destructive]") {
		t.Errorf("expected destructive marker in render:\n%s", body)
	}
	if !strings.Contains(body, "Generated by `kethosbase db diff`") {
		t.Errorf("expected header in render:\n%s", body)
	}
}
