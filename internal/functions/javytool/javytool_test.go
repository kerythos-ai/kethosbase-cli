package javytool

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPatchedJavyAssets_WellFormed checks the asset table is internally
// consistent: every entry names a gzip asset for the pinned Javy version, keys
// look like GOOS/GOARCH, and the current build's platform is present.
func TestPatchedJavyAssets_WellFormed(t *testing.T) {
	if len(patchedJavyAssets) == 0 {
		t.Fatal("patchedJavyAssets is empty")
	}
	for key, a := range patchedJavyAssets {
		if !strings.Contains(key, "/") {
			t.Errorf("asset key %q is not GOOS/GOARCH", key)
		}
		if a.file == "" {
			t.Errorf("%s: empty asset file name", key)
		}
		if !strings.HasPrefix(a.file, "javy-patched-"+JavyVersion+"-") ||
			!strings.HasSuffix(a.file, ".gz") {
			t.Errorf("%s: unexpected asset file name %q", key, a.file)
		}
		// sha256, when present, must be a 64-hex string.
		if a.sha256 != "" {
			if len(a.sha256) != 64 {
				t.Errorf("%s: sha256 %q is not 64 hex chars", key, a.sha256)
			}
			if _, err := hex.DecodeString(a.sha256); err != nil {
				t.Errorf("%s: sha256 %q is not hex: %v", key, a.sha256, err)
			}
		}
	}
	if _, ok := patchedJavyAssets[runtime.GOOS+"/"+runtime.GOARCH]; !ok {
		t.Errorf("no patched-Javy asset entry for the current platform %s/%s",
			runtime.GOOS, runtime.GOARCH)
	}
}

// TestPatchedJavyAssets_PublishedHaveChecksum documents which platforms are live.
// It never fails (a platform pending on CI legitimately has an empty sha256); it
// logs the split so the state is visible in test output.
func TestPatchedJavyAssets_PublishedHaveChecksum(t *testing.T) {
	var live, pending []string
	for key, a := range patchedJavyAssets {
		if a.sha256 != "" {
			live = append(live, key)
		} else {
			pending = append(pending, key)
		}
	}
	t.Logf("patched Javy published: %v", live)
	t.Logf("patched Javy pending on CI: %v", pending)
	if len(live) == 0 {
		t.Log("note: no platform has a published checksum yet")
	}
}

// TestDefaultBaseURL_HTTPS guards the security invariant that downloads are HTTPS.
func TestDefaultBaseURL_HTTPS(t *testing.T) {
	if !strings.HasPrefix(defaultJavyBaseURL, "https://") {
		t.Errorf("defaultJavyBaseURL must be https, got %q", defaultJavyBaseURL)
	}
	if !strings.Contains(defaultJavyBaseURL, patchedJavyReleaseTag) {
		t.Errorf("defaultJavyBaseURL %q should target release tag %q", defaultJavyBaseURL, patchedJavyReleaseTag)
	}
}

// TestDownloadVerifyGunzip verifies the download path: it fetches a gzipped
// payload, checks the checksum of the compressed bytes before writing, and
// gunzips to the destination. It also asserts a checksum mismatch is rejected
// and nothing is written.
func TestDownloadVerifyGunzip(t *testing.T) {
	payload := []byte("#!/bin/sh\necho patched-javy\n")
	var gzbuf bytes.Buffer
	gw := gzip.NewWriter(&gzbuf)
	if _, err := gw.Write(payload); err != nil {
		t.Fatal(err)
	}
	gw.Close()
	gzBytes := gzbuf.Bytes()
	sum := sha256.Sum256(gzBytes)
	wantSHA := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(gzBytes)
	}))
	defer srv.Close()

	t.Run("verifies and gunzips", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "javy")
		if err := downloadVerifyGunzip(srv.URL+"/asset.gz", wantSHA, dst); err != nil {
			t.Fatalf("downloadVerifyGunzip: %v", err)
		}
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("decompressed payload mismatch: got %q", got)
		}
	})

	t.Run("rejects checksum mismatch and writes nothing", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "javy")
		badSHA := strings.Repeat("00", 32)
		err := downloadVerifyGunzip(srv.URL+"/asset.gz", badSHA, dst)
		if err == nil {
			t.Fatal("expected checksum mismatch error, got nil")
		}
		if !strings.Contains(err.Error(), "checksum mismatch") {
			t.Errorf("expected checksum mismatch error, got: %v", err)
		}
		if _, statErr := os.Stat(dst); statErr == nil {
			t.Error("destination file should not exist after a mismatch")
		}
	})
}

// TestEnsureBinary_AutoDownloadsAndCaches exercises the full zero-setup path:
// with no KETHOSBASE_JAVY, ensureBinary downloads the platform asset over HTTPS
// from the configured base, verifies the pinned sha256, caches it, and returns
// the cached path on a second call without re-downloading.
func TestEnsureBinary_AutoDownloadsAndCaches(t *testing.T) {
	// A stand-in "javy" payload, gzipped, with its checksum registered for the
	// host platform so ensureBinary treats it as published.
	payload := []byte("#!/bin/sh\necho javy 9.0.0\n")
	var gzbuf bytes.Buffer
	gw := gzip.NewWriter(&gzbuf)
	gw.Write(payload)
	gw.Close()
	gzBytes := gzbuf.Bytes()
	sum := sha256.Sum256(gzBytes)
	sha := hex.EncodeToString(sum[:])

	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write(gzBytes)
	}))
	defer srv.Close()

	// Trust the TLS test server for this test only.
	old := httpClient
	httpClient = srv.Client()
	defer func() { httpClient = old }()

	key := runtime.GOOS + "/" + runtime.GOARCH
	saved := patchedJavyAssets[key]
	patchedJavyAssets[key] = patchedAsset{file: "javy-test.gz", sha256: sha}
	defer func() { patchedJavyAssets[key] = saved }()

	home := t.TempDir()
	t.Setenv("KETHOSBASE_JAVY", "")
	t.Setenv("KETHOSBASE_JAVY_BASE_URL", srv.URL) // https:// from httptest TLS
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	got, err := ensureBinary()
	if err != nil {
		t.Fatalf("ensureBinary (auto-download): %v", err)
	}
	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, payload) {
		t.Errorf("cached binary content mismatch: %q", b)
	}
	if hits != 1 {
		t.Errorf("expected exactly 1 download, got %d", hits)
	}

	// Second call must hit the cache, not the server.
	got2, err := ensureBinary()
	if err != nil {
		t.Fatalf("ensureBinary (cached): %v", err)
	}
	if got2 != got {
		t.Errorf("cache path changed: %q vs %q", got, got2)
	}
	if hits != 1 {
		t.Errorf("second call should not re-download; hits=%d", hits)
	}
}

// TestEnsureBinary_OverrideWins confirms KETHOSBASE_JAVY short-circuits the
// download path.
func TestEnsureBinary_OverrideWins(t *testing.T) {
	f := filepath.Join(t.TempDir(), "my-javy")
	if err := os.WriteFile(f, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KETHOSBASE_JAVY", f)
	got, err := ensureBinary()
	if err != nil {
		t.Fatalf("ensureBinary with override: %v", err)
	}
	if got != f {
		t.Errorf("override not honored: got %q want %q", got, f)
	}
}
