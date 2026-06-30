package gen

import (
	"strings"
	"testing"

	"github.com/kerythos-ai/kethosbase-cli/internal/introspect"
)

func TestTypeScript(t *testing.T) {
	s := &introspect.Schema{
		Name: "public",
		Enums: []introspect.Enum{
			{Name: "work_order_status", Labels: []string{"DRAFT", "PUBLISHED"}},
		},
		Tables: []introspect.Table{
			{Name: "work_orders", Columns: []introspect.Column{
				{Name: "id", UDTName: "uuid", HasDefault: true},
				{Name: "title", UDTName: "text"},
				{Name: "status", UDTName: "work_order_status", HasDefault: true},
				{Name: "skills", UDTName: "_text"},
				{Name: "pay_minor", UDTName: "int4"},
				{Name: "notes", UDTName: "text", Nullable: true},
			}},
		},
	}

	out := TypeScript(s)
	must := []string{
		`export type WorkOrderStatus = "DRAFT" | "PUBLISHED";`,
		"export interface Database {",
		"  public: {",
		"      work_orders: {",
		"          id: string;",            // Row: uuid → string
		"          status: WorkOrderStatus;", // Row: enum → alias
		"          skills: string[];",        // Row: _text → string[]
		"          pay_minor: number;",       // Row: int4 → number
		"          notes: string | null;",    // Row: nullable
		"          id?: string;",             // Insert: has default → optional
		"          title: string;",           // Insert: required
		"          notes?: string | null;",   // Insert: nullable → optional
		"          title?: string;",          // Update: all optional
		"      work_order_status: WorkOrderStatus;", // Enums map
	}
	for _, m := range must {
		if !strings.Contains(out, m) {
			t.Errorf("generated output missing:\n  %q\n--- full output ---\n%s", m, out)
		}
	}
}
