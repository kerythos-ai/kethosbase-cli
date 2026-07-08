// Package javytool manages the Javy CLI binary and the vendored Kethosbase Javy
// plugin, and drives `javy build` to compile a bundled JS file into a Wasm
// module.
//
// IMPORTANT — a PATCHED Javy is required. Javy v9.0.0 runs binaryen `wasm-opt`
// with the MVP feature set at both the plugin-initialization and the static
// `build` steps, and rejects the bulk-memory instructions that wasi-libc /
// QuickJS emit. We build a Javy patched to enable all wasm features at those two
// wasm-opt call sites (see plugin/javy-toolchain). The vendored plugin was
// produced with that patched Javy, and the static `build` step also needs it, so
// the runtime binary must be the patched build too. Provide it via the
// KETHOSBASE_JAVY environment variable (path to a patched `javy`); the stock
// upstream binary will fail the static build with a bulk-memory validator error.
//
// The plugin .wasm is vendored (embedded) in the CLI so no Rust toolchain is
// needed at runtime. When a Kethosbase-hosted patched-Javy release exists, the
// download path below can be pointed at it (checksum-verified like any asset).
package javytool

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// JavyVersion is the pinned Javy CLI release (paired with javy-plugin-api v7).
// The vendored plugin was built against this version; they must move together.
const JavyVersion = "v9.0.0"

// patchedJavyBaseURL, when set (via KETHOSBASE_JAVY_BASE_URL, or a future
// baked-in default once Kethosbase hosts the assets), is the base URL a
// gzipped, checksum-manifested patched-Javy binary is downloaded from. The stock
// upstream Javy is intentionally NOT used — its MVP wasm-opt rejects our output.
func patchedJavyBaseURL() string {
	return os.Getenv("KETHOSBASE_JAVY_BASE_URL")
}

// patchedAsset describes a platform's patched-Javy download and its checksum.
type patchedAsset struct {
	file   string // release asset name (a gzipped binary)
	sha256 string // sha256 of the gzipped asset
}

// patchedJavyAssets maps GOOS/GOARCH to the Kethosbase-hosted patched-Javy
// asset. Populate the checksums when the assets are published in CI (built by
// plugin/javy-toolchain across the target matrix). Empty until then.
var patchedJavyAssets = map[string]patchedAsset{}

// Tool resolves and runs the pinned Javy CLI.
type Tool struct {
	binPath    string
	pluginPath string
}

// Ensure resolves the Javy binary (downloading+verifying on first use unless
// KETHOSBASE_JAVY overrides it) and materializes the vendored plugin to disk.
func Ensure() (*Tool, error) {
	// Materialize the vendored plugin first: it is cheap and, if missing, avoids
	// a pointless Javy download.
	plugin, err := ensurePlugin()
	if err != nil {
		return nil, err
	}
	bin, err := ensureBinary()
	if err != nil {
		return nil, err
	}
	return &Tool{binPath: bin, pluginPath: plugin}, nil
}

// Build compiles a JS file into a Wasm module using the vendored plugin, writing
// to outPath. It shells out to `javy build -C plugin=<plugin> -o <out> <in>`.
func (t *Tool) Build(jsPath, outPath string) error {
	cmd := exec.Command(t.binPath, "build", "-C", "plugin="+t.pluginPath, "-o", outPath, jsPath)
	var stderr, stdout []byte
	cmd.Stdout = writerFunc(func(p []byte) (int, error) { stdout = append(stdout, p...); return len(p), nil })
	cmd.Stderr = writerFunc(func(p []byte) (int, error) { stderr = append(stderr, p...); return len(p), nil })
	if err := cmd.Run(); err != nil {
		msg := string(stderr)
		if msg == "" {
			msg = string(stdout)
		}
		return fmt.Errorf("javy build failed: %v: %s", err, msg)
	}
	return nil
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

func toolsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".kethosbase", "tools"), nil
}

// ensureBinary returns the path to a PATCHED Javy binary. Resolution order:
//
//  1. KETHOSBASE_JAVY — an explicit path to a patched `javy` (no checksum
//     enforced; the operator vouches for it). This is the supported path today.
//  2. A cached patched binary previously placed in ~/.kethosbase/tools.
//
// A Kethosbase-hosted, checksum-verified patched-Javy release is the intended
// future default (downloadVerifyGunzip + patchedJavyAssets are ready for it);
// until those assets exist we do NOT fall back to the stock upstream binary,
// because the stock `javy build` rejects the bulk-memory output with a MVP
// wasm-opt validator error and would mislead the user.
func ensureBinary() (string, error) {
	if override := os.Getenv("KETHOSBASE_JAVY"); override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("KETHOSBASE_JAVY=%q: %w", override, err)
		}
		return override, nil
	}

	dir, err := toolsDir()
	if err != nil {
		return "", err
	}
	name := "javy-patched-" + JavyVersion
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dst := filepath.Join(dir, name)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil // previously provisioned
	}

	// If a patched-Javy release is configured, download+verify it (checksum
	// enforced before the binary is written).
	key := runtime.GOOS + "/" + runtime.GOARCH
	if base := patchedJavyBaseURL(); base != "" {
		if a, ok := patchedJavyAssets[key]; ok && a.sha256 != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", err
			}
			url := strings.TrimRight(base, "/") + "/" + a.file
			if err := downloadVerifyGunzip(url, a.sha256, dst); err != nil {
				return "", fmt.Errorf("download patched Javy: %w", err)
			}
			if runtime.GOOS != "windows" {
				if err := os.Chmod(dst, 0o755); err != nil {
					return "", err
				}
			}
			return dst, nil
		}
	}

	return "", fmt.Errorf(
		"no patched Javy available for %s.\n"+
			"Javy %s needs a small wasm-opt patch (enable all wasm features) to compile\n"+
			"Functions; build one with plugin/javy-toolchain/build-javy-and-plugin.sh and\n"+
			"point KETHOSBASE_JAVY at it, or drop it at %s.",
		key, JavyVersion, dst)
}

// downloadVerifyGunzip fetches a gzipped asset, verifies the gzip bytes against
// wantSHA, then decompresses to dst. Verifying before decompressing means a
// tampered or truncated download never reaches disk as an executable.
func downloadVerifyGunzip(url, wantSHA, dst string) error {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	gzBytes, err := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(gzBytes)
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, wantSHA)
	}

	gz, err := gzip.NewReader(bytesReader(gzBytes))
	if err != nil {
		return err
	}
	defer gz.Close()

	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, gz); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

var errNoPlugin = errors.New("vendored Javy plugin is missing from this build")

// bytesReader avoids importing bytes just for a reader in two spots.
func bytesReader(b []byte) io.Reader { return &sliceReader{b: b} }

type sliceReader struct {
	b   []byte
	off int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.off >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.off:])
	r.off += n
	return n, nil
}
