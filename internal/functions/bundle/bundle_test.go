package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A minimal function that imports the SDK and uses serve/db/log — the same
// shape the dry-run smoke test compiles.
const sampleTS = `import { serve, db, log } from "@kethosbase/functions";

serve(async (req) => {
  log("hello from a function", req.method, req.path);
  const rows = db.query("select 1 as one");
  return { status: 200, body: JSON.stringify({ ok: true, rows }) };
});
`

func TestBundle_ResolvesSDKShimAndInlinesBridge(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "hello.ts")
	if err := os.WriteFile(entry, []byte(sampleTS), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Bundle(entry)
	if err != nil {
		t.Fatalf("Bundle() error: %v", err)
	}
	js := string(res.JS)

	// The bundle must be self-contained: the bare SDK import must be gone...
	if strings.Contains(js, sdkModule) && strings.Contains(js, "import ") {
		t.Errorf("bundle still references an unresolved %q import", sdkModule)
	}
	// ...and it must call the plugin bridge globals the shim uses. serve() calls
	// __kb_read_request/__kb_write_response; db.query calls __kb_db_query.
	for _, want := range []string{"__kb_read_request", "__kb_write_response", "__kb_db_query", "__kb_log"} {
		if !strings.Contains(js, want) {
			t.Errorf("bundle missing bridge global %q", want)
		}
	}
}

// TestBundle_ShimIsQuickJSSafe guards against the shim (or a user's minimal
// function using only the shim) referencing browser/Node globals that QuickJS —
// the Javy runtime — does not provide. Referencing any of these (e.g. evaluating
// `new TextEncoder()` at module scope) traps the module at boot, before it can
// read the request. The shim must implement UTF-8 and base64 in pure JS.
func TestBundle_ShimIsQuickJSSafe(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "hello.ts")
	if err := os.WriteFile(entry, []byte(sampleTS), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Bundle(entry)
	if err != nil {
		t.Fatalf("Bundle() error: %v", err)
	}
	js := string(res.JS)

	// None of these identifiers may appear in the bundled shim output. (The SDK
	// exposes its own `fetch` *function*, which routes through __kb_fetch; that is
	// a definition, not a reference to the absent global, so it is not listed.)
	forbidden := []string{"TextEncoder", "TextDecoder", "atob(", "btoa(", "Buffer", "crypto"}
	for _, bad := range forbidden {
		if strings.Contains(js, bad) {
			t.Errorf("bundled shim references QuickJS-absent global %q; use pure-JS UTF-8/base64 instead", bad)
		}
	}
}

func TestBundle_MissingEntrypoint(t *testing.T) {
	if _, err := Bundle(filepath.Join(t.TempDir(), "nope.ts")); err == nil {
		t.Fatal("expected error for missing entrypoint")
	}
}
