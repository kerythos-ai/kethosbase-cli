package javytool

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestDownloadRealPatchedJavy_Integration downloads the REAL published patched
// Javy for the host platform, verifies the pinned checksum, and confirms the
// binary runs (`javy --version`). It is opt-in (KETHOSBASE_JAVY_IT=1) and skips
// when the host platform's asset has no published checksum yet, so the default
// `go test ./...` stays hermetic and offline-safe.
func TestDownloadRealPatchedJavy_Integration(t *testing.T) {
	if os.Getenv("KETHOSBASE_JAVY_IT") == "" {
		t.Skip("set KETHOSBASE_JAVY_IT=1 to run the network integration test")
	}
	key := runtime.GOOS + "/" + runtime.GOARCH
	a, ok := patchedJavyAssets[key]
	if !ok || a.sha256 == "" {
		t.Skipf("no published patched Javy for %s yet", key)
	}
	// Use a clean HOME so ensureBinary actually downloads (no cache, no override).
	t.Setenv("KETHOSBASE_JAVY", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	bin, err := ensureBinary()
	if err != nil {
		t.Fatalf("ensureBinary (download real asset): %v", err)
	}
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("running downloaded javy: %v (%s)", err, out)
	}
	if !strings.Contains(string(out), "javy") {
		t.Errorf("unexpected javy --version output: %q", out)
	}
	t.Logf("downloaded + ran patched Javy: %s", strings.TrimSpace(string(out)))
}
