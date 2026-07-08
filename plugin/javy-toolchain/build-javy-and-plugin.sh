#!/usr/bin/env bash
# Reproducible build of (a) the patched Javy CLI and (b) the initialized
# Kethosbase Javy plugin, inside a Linux container. Run from the repo root:
#
#   docker run --rm -v "$PWD:/repo" -w /repo rust:1-bookworm \
#     bash plugin/javy-toolchain/build-javy-and-plugin.sh
#
# Outputs (under plugin/javy-toolchain/out/):
#   kethosbase-plugin.wasm      -> copy to internal/functions/javytool/build/plugin/
#   javy-patched-<triple>       -> the patched Javy CLI for this container's target
#
# WHY A PATCH: Javy v9.0.0 runs binaryen `wasm-opt` with the MVP feature set at
# BOTH the `init-plugin` step (plugin-processing) and the static `build` step
# (codegen). wasi-libc / QuickJS emit bulk-memory ops (memory.copy/fill, active
# data-segment flags), which MVP wasm-opt rejects. We enable `.all_features()`
# at both call sites so wasm-opt reads/validates/optimizes post-MVP modules.
# The runtime binary must be the patched one for the static `build` to succeed;
# stock Javy only works for dynamic linking (which is not self-contained).
set -euo pipefail

JAVY_TAG="v9.0.0"
here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
work="$here/work"
out="$here/out"
mkdir -p "$work" "$out"

export DEBIAN_FRONTEND=noninteractive CARGO_TERM_COLOR=never
echo "==> system deps"
apt-get update -qq
apt-get install -y -qq git curl xz-utils clang llvm libclang-dev pkg-config cmake build-essential ca-certificates python3 >/dev/null

echo "==> wasm target"
rustup target add wasm32-wasip1 >/dev/null

echo "==> clone Javy $JAVY_TAG"
[ -d "$work/javy" ] || git clone --depth 1 --branch "$JAVY_TAG" \
  https://github.com/bytecodealliance/javy.git "$work/javy" >/dev/null 2>&1

echo "==> patch wasm-opt call sites to enable all features"
python3 "$here/patch-javy.py" "$work/javy"

echo "==> build patched Javy (default plugin first, then CLI)"
( cd "$work/javy" && cargo build --release --target=wasm32-wasip1 -p javy-plugin >/dev/null )
( cd "$work/javy" && cargo build --release -p javy-cli --bin javy >/dev/null )
JAVY="$work/javy/target/release/javy"
"$JAVY" --version

echo "==> build the Kethosbase plugin"
( cd "$repo/plugin" && cargo build --release --target=wasm32-wasip1 >/dev/null )
RAW="$repo/plugin/target/wasm32-wasip1/release/kethosbase_javy_plugin.wasm"

echo "==> init-plugin (patched Javy)"
"$JAVY" init-plugin "$RAW" -o "$out/kethosbase-plugin.wasm"

triple="$(rustc -vV | sed -n 's/host: //p')"
cp "$JAVY" "$out/javy-patched-$triple"

echo "==> checksums"
( cd "$out" && sha256sum kethosbase-plugin.wasm "javy-patched-$triple" )
echo "==> done. Copy $out/kethosbase-plugin.wasm to internal/functions/javytool/build/plugin/"
