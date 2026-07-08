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

func TestBundle_MissingEntrypoint(t *testing.T) {
	if _, err := Bundle(filepath.Join(t.TempDir(), "nope.ts")); err == nil {
		t.Fatal("expected error for missing entrypoint")
	}
}
