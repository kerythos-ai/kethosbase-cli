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
// the runtime binary must be the patched build too. The stock upstream binary
// would fail the static build with a bulk-memory validator error.
//
// The patched Javy is provisioned with ZERO manual setup: on first use the CLI
// downloads the binary for the host platform from the Kethosbase release
// (.github/workflows/javy-patched.yml builds it on native runners), verifies its
// pinned sha256 before use, and caches it under ~/.kethosbase/tools. Set
// KETHOSBASE_JAVY to a local patched `javy` to override (air-gapped/dev), or
// KETHOSBASE_JAVY_BASE_URL to fetch from a mirror.
//
// The plugin .wasm is vendored (embedded) in the CLI so no Rust toolchain is
// ever needed at runtime.
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

// patchedJavyReleaseTag is the GitHub release the patched-Javy binaries are
// published under (by .github/workflows/javy-patched.yml). It embeds the pinned
// upstream Javy version so a version bump moves the tag too.
const patchedJavyReleaseTag = "javy-patched-" + JavyVersion

// defaultJavyBaseURL is the baked-in release base the CLI downloads the patched
// Javy binary from when the user has not configured one. Override with
// KETHOSBASE_JAVY_BASE_URL (e.g. for a mirror or an air-gapped cache).
const defaultJavyBaseURL = "https://github.com/kerythos-ai/kethosbase-cli/releases/download/" + patchedJavyReleaseTag

// patchedJavyBaseURL is where the gzipped, checksum-pinned patched-Javy binary
// is fetched from. The stock upstream Javy is intentionally NOT used — its MVP
// wasm-opt rejects our bulk-memory output.
func patchedJavyBaseURL() string {
	if v := os.Getenv("KETHOSBASE_JAVY_BASE_URL"); v != "" {
		return v
	}
	return defaultJavyBaseURL
}

// patchedAsset describes a platform's patched-Javy download and its checksum.
type patchedAsset struct {
	file   string // release asset name (a gzipped binary)
	sha256 string // sha256 of the gzipped asset
}

// patchedJavyAssets maps GOOS/GOARCH to the Kethosbase-hosted patched-Javy asset
// (built by .github/workflows/javy-patched.yml on native runners). The file name
// is javy-patched-<JavyVersion>-<rust-triple>.gz. A platform with an empty
// sha256 is not yet published (its CI leg is pending) and the CLI reports it as
// such rather than downloading an unverified binary.
var patchedJavyAssets = map[string]patchedAsset{
	"linux/amd64": {
		file:   "javy-patched-" + JavyVersion + "-x86_64-unknown-linux-gnu.gz",
		sha256: "5ee5cb6172c0a21699bac873532de85f05a0322da48205b03b1b874042b52cbf",
	},
	"linux/arm64": {
		file:   "javy-patched-" + JavyVersion + "-aarch64-unknown-linux-gnu.gz",
		sha256: "", // filled from the published linux/arm64 asset
	},
	"darwin/amd64": {
		file:   "javy-patched-" + JavyVersion + "-x86_64-apple-darwin.gz",
		sha256: "", // pending CI (macos-13 runner)
	},
	"darwin/arm64": {
		file:   "javy-patched-" + JavyVersion + "-aarch64-apple-darwin.gz",
		sha256: "", // pending CI (macos-latest runner)
	},
	"windows/amd64": {
		file:   "javy-patched-" + JavyVersion + "-x86_64-pc-windows-msvc.gz",
		sha256: "", // pending CI (windows-latest runner)
	},
}

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

// ensureBinary returns the path to a PATCHED Javy binary, requiring no manual
// setup. Resolution order:
//
//  1. KETHOSBASE_JAVY — an explicit path to a patched `javy` (no checksum
//     enforced; the operator vouches for it). Escape hatch / air-gapped use.
//  2. A previously-cached patched binary under the user cache dir.
//  3. Download the platform's patched-Javy asset from the Kethosbase release,
//     verifying the pinned sha256 before the binary is written or used.
//
// The stock upstream Javy is never used: its MVP wasm-opt rejects the
// bulk-memory output with a validator error and would mislead the user.
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

	key := runtime.GOOS + "/" + runtime.GOARCH
	a, ok := patchedJavyAssets[key]
	if !ok {
		return "", fmt.Errorf(
			"no patched Javy is available for %s. Build one with "+
				"plugin/javy-toolchain/build-javy-and-plugin.sh and set KETHOSBASE_JAVY to it.",
			key)
	}
	if a.sha256 == "" {
		return "", fmt.Errorf(
			"the patched Javy for %s has not been published yet (its CI build is pending).\n"+
				"Build one locally with plugin/javy-toolchain/build-javy-and-plugin.sh and set KETHOSBASE_JAVY to it.",
			key)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	url := strings.TrimRight(patchedJavyBaseURL(), "/") + "/" + a.file
	if !strings.HasPrefix(url, "https://") {
		return "", fmt.Errorf("refusing non-HTTPS Javy download URL %q", url)
	}
	if err := downloadVerifyGunzip(url, a.sha256, dst); err != nil {
		return "", fmt.Errorf("download patched Javy for %s: %w", key, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dst, 0o755); err != nil {
			return "", err
		}
	}
	return dst, nil
}

// httpClient performs the download. It is a package variable so tests can inject
// a client that trusts a local TLS test server; production uses a plain client
// with a timeout.
var httpClient = &http.Client{Timeout: 120 * time.Second}

// downloadVerifyGunzip fetches a gzipped asset, verifies the gzip bytes against
// wantSHA, then decompresses to dst. Verifying before decompressing means a
// tampered or truncated download never reaches disk as an executable.
func downloadVerifyGunzip(url, wantSHA, dst string) error {
	resp, err := httpClient.Get(url)
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
