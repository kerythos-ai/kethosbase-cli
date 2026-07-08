#!/usr/bin/env bash
# Build the patched Javy CLI inside a Linux container and emit a gzipped asset
# named by the container's target triple, plus its .sha256, into /out. Used to
# SEED the linux legs of the patched-Javy release locally (via Docker), before
# the GitHub Actions matrix (.github/workflows/javy-patched.yml) covers all
# platforms on native runners.
#
# Requires two bind mounts: /out (destination) and /patch (the patch-javy.py
# script). Register arm64 emulation first if building linux/arm64:
#   docker run --rm --privileged tonistiigi/binfmt --install arm64
#
# linux/amd64:
#   docker run --rm --platform linux/amd64 \
#     -v "$PWD/plugin/javy-toolchain:/patch" -v "$OUTDIR:/out" \
#     rust:1-bookworm bash /out/build-patched-javy.sh
# linux/arm64: same with --platform linux/arm64.
#
# After building, upload the .gz + .gz.sha256 to the release tag
# javy-patched-v9.0.0 and copy the sha256 into javytool.patchedJavyAssets.
set -euo pipefail
JAVY_TAG="v9.0.0"
export DEBIAN_FRONTEND=noninteractive CARGO_TERM_COLOR=never

apt-get update -qq
apt-get install -y -qq git clang llvm libclang-dev pkg-config cmake build-essential ca-certificates python3 gzip coreutils >/dev/null
rustup target add wasm32-wasip1 >/dev/null

work=/tmp/javybuild
mkdir -p "$work"
cd "$work"
[ -d javy ] || git clone --depth 1 --branch "$JAVY_TAG" https://github.com/bytecodealliance/javy.git javy >/dev/null 2>&1

python3 /patch/patch-javy.py "$work/javy"

# The CLI build needs the default plugin built first (build.rs copies it).
( cd javy && cargo build --release --target=wasm32-wasip1 -p javy-plugin >/dev/null )
( cd javy && cargo build --release -p javy-cli --bin javy >/dev/null )

triple="$(rustc -vV | sed -n 's/host: //p')"
bin="javy/target/release/javy"
"$bin" --version

asset="/out/javy-patched-${JAVY_TAG}-${triple}.gz"
gzip -c "$bin" > "$asset"
sha256sum "$asset" | awk '{print $1}' > "$asset.sha256"
echo "WROTE $asset"
cat "$asset.sha256"
