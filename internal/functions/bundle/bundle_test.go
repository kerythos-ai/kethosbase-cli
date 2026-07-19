package bundle

import (
	"os"
	"path/filepath"
	"regexp"
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

// A function that signs with a host-held key (ADR-0126): the key is named by a
// Function secret, never carried as bytes.
const sampleSignTS = `import { serve, crypto, keyFromSecret, log } from "@kethosbase/functions";

serve(async (req) => {
  const key = keyFromSecret("FCM_SERVICE_ACCOUNT_KEY");
  const sig = await crypto.subtle.sign({ name: "RSASSA-PKCS1-v1_5" }, key, "header.payload");
  log("signed", sig.byteLength);
  return { status: 200, body: "ok" };
});
`

// bundleSource bundles src through the shim and returns the output JS.
func bundleSource(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	entry := filepath.Join(dir, "hello.ts")
	if err := os.WriteFile(entry, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Bundle(entry)
	if err != nil {
		t.Fatalf("Bundle() error: %v", err)
	}
	return string(res.JS)
}

// TestBundle_InlinesSignBridge covers the signing half of the bridge contract:
// crypto.subtle.sign must route through the plugin's __kb_sign global.
func TestBundle_InlinesSignBridge(t *testing.T) {
	js := bundleSource(t, sampleSignTS)

	if strings.Contains(js, sdkModule) && strings.Contains(js, "import ") {
		t.Errorf("bundle still references an unresolved %q import", sdkModule)
	}
	if !strings.Contains(js, "__kb_sign") {
		t.Error("bundle missing bridge global \"__kb_sign\"")
	}
	// The shim defines `crypto` itself (like `fetch`) rather than reading the
	// absent QuickJS global; the declaration is what makes it safe to reference.
	if !regexp.MustCompile(`\b(var|let|const)\s+crypto\b`).MatchString(js) {
		t.Error("bundle references `crypto` without declaring it; it must be shim-defined, not the absent QuickJS global")
	}
}

// TestBundle_SignPathKeepsKeyMaterialOutOfTheRuntime guards the load-bearing
// security property of ADR-0126: the guest only ever names a secret, and the
// host unseals the PEM and signs in host memory. A key-by-value path here (an
// importKey()-style API taking PEM/DER/JWK) would put a private key into WASM
// linear memory and defeat the whole design, so it must not appear by accident
// — landing one is a deliberate decision that should break this test first.
func TestBundle_SignPathKeepsKeyMaterialOutOfTheRuntime(t *testing.T) {
	js := bundleSource(t, sampleSignTS)

	for _, bad := range []string{"importKey", "PRIVATE KEY", "pkcs8", "generateKey"} {
		if strings.Contains(js, bad) {
			t.Errorf("shim sign path contains %q; keys must be named by secret, never carried as material", bad)
		}
	}
	// Passing raw material must be refused with a pointer at the right API.
	if !strings.Contains(js, "keyFromSecret") {
		t.Error("shim sign path does not mention keyFromSecret; raw-key rejection must name the supported API")
	}
}

// TestBundle_SignShimIsQuickJSSafe is TestBundle_ShimIsQuickJSSafe for the sign
// path, which pulls in shim code the plain sample tree-shakes away. It cannot
// reuse that test's bare substring list: `crypto` is legitimately *defined* by
// the shim here, and `Buffer` would false-positive on `ArrayBuffer` (a real
// ES2015 global QuickJS does provide). So it matches the absent globals
// precisely instead.
func TestBundle_SignShimIsQuickJSSafe(t *testing.T) {
	js := bundleSource(t, sampleSignTS)

	for _, bad := range []string{"TextEncoder", "TextDecoder", "atob(", "btoa("} {
		if strings.Contains(js, bad) {
			t.Errorf("bundled shim references QuickJS-absent global %q", bad)
		}
	}
	// Node's Buffer, but not ArrayBuffer/SharedArrayBuffer.
	if regexp.MustCompile(`\bBuffer\b`).MatchString(js) {
		t.Error("bundled shim references QuickJS-absent global \"Buffer\"")
	}
	// The shim must never READ crypto off the host global; it defines its own.
	for _, bad := range []string{"globalThis.crypto", "window.crypto", "self.crypto", "crypto.getRandomValues", "crypto.randomUUID"} {
		if strings.Contains(js, bad) {
			t.Errorf("bundled shim reads absent global crypto via %q; the shim must define its own", bad)
		}
	}
}

func TestBundle_MissingEntrypoint(t *testing.T) {
	if _, err := Bundle(filepath.Join(t.TempDir(), "nope.ts")); err == nil {
		t.Fatal("expected error for missing entrypoint")
	}
}
