package javytool

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// pluginWasm is the initialized Kethosbase Javy plugin, built from ../../../plugin
// via plugin/build.sh and committed at build/plugin/kethosbase-plugin.wasm. It is
// embedded so `functions deploy` needs no Rust toolchain at runtime.
//
// The file is checked in; if a build is produced before the artifact has been
// generated the embed still succeeds (an empty file), and ensurePlugin reports a
// clear error pointing the maintainer at plugin/build.sh.
//
//go:embed build/plugin/kethosbase-plugin.wasm
var pluginWasm []byte

// ensurePlugin writes the embedded plugin to the tools cache and returns its
// path. Javy's `-C plugin=` flag takes a filesystem path, so we materialize it.
func ensurePlugin() (string, error) {
	if len(pluginWasm) == 0 {
		return "", fmt.Errorf("%w: rebuild it with plugin/build.sh and commit build/plugin/kethosbase-plugin.wasm", errNoPlugin)
	}
	dir, err := toolsDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Name the on-disk copy by Javy version so a version bump re-materializes it.
	dst := filepath.Join(dir, "kethosbase-plugin-"+JavyVersion+".wasm")
	// Rewrite if size differs (cheap staleness check) or missing.
	if fi, err := os.Stat(dst); err == nil && fi.Size() == int64(len(pluginWasm)) {
		return dst, nil
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, pluginWasm, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dst, nil
}
