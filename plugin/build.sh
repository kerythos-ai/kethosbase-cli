#!/usr/bin/env bash
# Build the vendored Kethosbase Javy plugin .wasm.
#
# Prerequisites:
#   - rustup with the wasm32-wasip1 target: `rustup target add wasm32-wasip1`
#   - the pinned Javy CLI (v9.0.0) on PATH, or set JAVY to its path.
#
# Produces internal/functions/build/plugin/kethosbase-plugin.wasm and updates
# its sha256. This is a one-time/occasional build step for maintainers; the
# `functions deploy` command uses the committed artifact.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
out_dir="$here/../internal/functions/javytool/build/plugin"
out="$out_dir/kethosbase-plugin.wasm"
javy="${JAVY:-javy}"

mkdir -p "$out_dir"

echo "==> cargo build --target=wasm32-wasip1 --release"
( cd "$here" && cargo build --target=wasm32-wasip1 --release )

raw="$here/target/wasm32-wasip1/release/kethosbase_javy_plugin.wasm"

echo "==> javy init-plugin"
"$javy" init-plugin "$raw" -o "$out"

echo "==> sha256"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "$out" | awk '{print $1}' > "$out.sha256"
else
  shasum -a 256 "$out" | awk '{print $1}' > "$out.sha256"
fi

echo "Wrote $out"
echo "sha256: $(cat "$out.sha256")"
