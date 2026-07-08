package wasmcheck

import (
	"encoding/binary"
	"os"
	"testing"
)

// --- tiny hand-rolled Wasm builder for deterministic fixtures ---

func uvarint(n uint64) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n != 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			return out
		}
	}
}

func vecName(s string) []byte {
	return append(uvarint(uint64(len(s))), s...)
}

func section(id byte, payload []byte) []byte {
	out := []byte{id}
	out = append(out, uvarint(uint64(len(payload)))...)
	return append(out, payload...)
}

// buildModule assembles a minimal module with the given func imports and
// exports. Every import is a func import (kind 0x00, typeidx 0); every export
// is a func export (kind 0x00, index 0). A single empty type is declared so the
// indices are valid enough for our parser (we never instantiate).
func buildModule(imports []Import, exports []string) []byte {
	var mod []byte
	mod = append(mod, 0x00, 0x61, 0x73, 0x6d) // magic
	v := make([]byte, 4)
	binary.LittleEndian.PutUint32(v, 1)
	mod = append(mod, v...) // version 1

	// Type section: one type () -> ().
	typePayload := append(uvarint(1), 0x60) // 1 type, functype tag
	typePayload = append(typePayload, uvarint(0)...)
	typePayload = append(typePayload, uvarint(0)...)
	mod = append(mod, section(1, typePayload)...)

	// Import section.
	if len(imports) > 0 {
		imp := uvarint(uint64(len(imports)))
		for _, im := range imports {
			imp = append(imp, vecName(im.Module)...)
			imp = append(imp, vecName(im.Name)...)
			imp = append(imp, 0x00)          // func import
			imp = append(imp, uvarint(0)...) // typeidx 0
		}
		mod = append(mod, section(2, imp)...)
	}

	// Export section.
	if len(exports) > 0 {
		exp := uvarint(uint64(len(exports)))
		for _, e := range exports {
			exp = append(exp, vecName(e)...)
			exp = append(exp, 0x00)          // func export
			exp = append(exp, uvarint(0)...) // index 0
		}
		mod = append(mod, section(7, exp)...)
	}
	return mod
}

func TestValidate_AllowedImportsAndStart(t *testing.T) {
	mod := buildModule(
		[]Import{
			{Module: "wasi_snapshot_preview1", Name: "fd_read"},
			{Module: "wasi_snapshot_preview1", Name: "fd_write"},
			{Module: "kethosbase", Name: "kb_db_query"},
			{Module: "kethosbase", Name: "kb_read"},
			{Module: "kethosbase", Name: "kb_log"},
		},
		[]string{"memory", "_start"},
	)

	info, err := Validate(mod)
	if err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}
	// Import namespaces must be exactly {kethosbase, wasi_snapshot_preview1}.
	want := map[string]bool{"kethosbase": true, "wasi_snapshot_preview1": true}
	if len(info.ImportModules) != len(want) {
		t.Fatalf("import modules = %v, want keys %v", info.ImportModules, want)
	}
	for _, m := range info.ImportModules {
		if !want[m] {
			t.Errorf("unexpected import module %q", m)
		}
	}
	if !info.HasExport("_start") {
		t.Errorf("expected _start export, got %v", info.Exports)
	}
}

func TestValidate_RejectsForeignImport(t *testing.T) {
	mod := buildModule(
		[]Import{
			{Module: "kethosbase", Name: "kb_log"},
			{Module: "env", Name: "sneaky"}, // not allowed
		},
		[]string{"_start"},
	)
	if _, err := Validate(mod); err == nil {
		t.Fatal("expected error for foreign import module, got nil")
	}
}

func TestValidate_RejectsMissingStart(t *testing.T) {
	mod := buildModule(
		[]Import{{Module: "kethosbase", Name: "kb_log"}},
		[]string{"memory"}, // no _start
	)
	if _, err := Validate(mod); err == nil {
		t.Fatal("expected error for missing _start export, got nil")
	}
}

func TestInspect_RejectsNonWasm(t *testing.T) {
	if _, err := Inspect([]byte("not wasm at all")); err == nil {
		t.Fatal("expected error for non-wasm input")
	}
}

// TestValidate_RealWASIModule inspects a real GOOS=wasip1 module that imports
// the kethosbase host functions and wasi, exercising the parser against genuine
// (not hand-built) binary structure — the same shape a deployed function has.
func TestValidate_RealWASIModule(t *testing.T) {
	wasm, err := readTestdata("sample-kethosbase.wasm")
	if err != nil {
		t.Skipf("sample module unavailable: %v", err)
	}
	info, err := Validate(wasm)
	if err != nil {
		t.Fatalf("Validate(real module) error: %v", err)
	}
	want := map[string]bool{"kethosbase": true, "wasi_snapshot_preview1": true}
	for _, m := range info.ImportModules {
		if !want[m] {
			t.Errorf("real module has disallowed import namespace %q", m)
		}
	}
	// It must import at least one kethosbase.* function and export _start.
	var sawKB bool
	for _, im := range info.Imports {
		if im.Module == "kethosbase" {
			sawKB = true
		}
	}
	if !sawKB {
		t.Error("expected at least one kethosbase.* import")
	}
	if !info.HasExport("_start") {
		t.Error("expected _start export")
	}
}

func readTestdata(name string) ([]byte, error) {
	return os.ReadFile("testdata/" + name)
}
