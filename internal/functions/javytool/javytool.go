// Package javytool manages the pinned Javy CLI binary and the vendored
// Kethosbase Javy plugin, and drives `javy build` to compile a bundled JS file
// into a Wasm module.
//
// The Javy binary is downloaded on first use from the Bytecode Alliance GitHub
// release, verified against a pinned SHA-256, and cached under the user's home
// (~/.kethosbase/tools). The plugin .wasm is vendored (embedded) in the binary
// so no Rust toolchain is ever needed at runtime. Downloads are gzip; we verify
// the checksum of the compressed asset (matching the checksums the Javy project
// publishes) before decompressing.
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
	"time"
)

// JavyVersion is the pinned Javy CLI release (paired with javy-plugin-api v7).
// The vendored plugin was built against this version; they must move together.
const JavyVersion = "v9.0.0"

const releaseBase = "https://github.com/bytecodealliance/javy/releases/download/" + JavyVersion

// asset describes a platform's Javy binary download and its pinned checksum.
type asset struct {
	file   string // release asset name (a gzipped binary)
	sha256 string // sha256 of the gzipped asset
}

// javyAssets maps GOOS/GOARCH to the pinned release asset. Checksums are the
// published .sha256 values for Javy v9.0.0.
var javyAssets = map[string]asset{
	"windows/amd64": {"javy-x86_64-windows-" + JavyVersion + ".gz", "8de6e4b90391c73c9c787043e111b9b2dd16d0ac677099dffd0d8b33dc839154"},
	"windows/arm64": {"javy-arm-windows-" + JavyVersion + ".gz", "d53737c35ed702bd65d836ea8a237bf20f7f3a7a7bf2171b903fa6998a038a6a"},
	"linux/amd64":   {"javy-x86_64-linux-" + JavyVersion + ".gz", "51a240468da9ebfebeb4292db635e2fab58ea01b9b81832001f780a05dbb744b"},
	"linux/arm64":   {"javy-arm-linux-" + JavyVersion + ".gz", "1ec90c7ada039cab39e79d1377ad72bd80a20c9c07c714741568090fbb870b1a"},
	"darwin/amd64":  {"javy-x86_64-macos-" + JavyVersion + ".gz", "7bb6a868e0fb9015814be67ed6df90fbbe5b515f1bc657e06412a2a4dd690987"},
	"darwin/arm64":  {"javy-arm-macos-" + JavyVersion + ".gz", "86e4490a55f47c3fd76966e32edce1bec5e97c2e9d1627697fa8e840785fdd4c"},
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

// ensureBinary returns the path to the pinned Javy binary, downloading and
// verifying it if it is not already cached. Set KETHOSBASE_JAVY to use a local
// javy binary (e.g. for maintainers or air-gapped builds); no checksum is
// enforced on an explicitly provided binary.
func ensureBinary() (string, error) {
	if override := os.Getenv("KETHOSBASE_JAVY"); override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("KETHOSBASE_JAVY=%q: %w", override, err)
		}
		return override, nil
	}

	key := runtime.GOOS + "/" + runtime.GOARCH
	a, ok := javyAssets[key]
	if !ok {
		return "", fmt.Errorf("no pinned Javy binary for %s; set KETHOSBASE_JAVY to a local javy %s binary", key, JavyVersion)
	}

	dir, err := toolsDir()
	if err != nil {
		return "", err
	}
	name := "javy-" + JavyVersion
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dst := filepath.Join(dir, name)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil // already cached
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	url := releaseBase + "/" + a.file
	if err := downloadVerifyGunzip(url, a.sha256, dst); err != nil {
		return "", fmt.Errorf("download Javy %s: %w", JavyVersion, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dst, 0o755); err != nil {
			return "", err
		}
	}
	return dst, nil
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
