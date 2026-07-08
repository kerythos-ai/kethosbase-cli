# Patched Javy toolchain

Javy v9.0.0 runs binaryen `wasm-opt` with the **MVP** feature set at two call
sites — plugin initialization (`crates/plugin-processing`) and the static `build`
(`crates/codegen`) — and rejects the bulk-memory ops that wasi-libc / QuickJS
emit. `patch-javy.py` adds `.all_features()` to both `OptimizationOptions` chains
so a patched Javy accepts them. Everything here produces that patched Javy and the
initialized Kethosbase plugin.

## Scripts

| Script | Purpose |
|--------|---------|
| `patch-javy.py <javy-src>` | Idempotently add `.all_features()` to both wasm-opt call sites in a Javy checkout. |
| `build-javy-and-plugin.sh` | One-shot (in a `rust:1-bookworm` container): clone+patch+build Javy, build the plugin, run `init-plugin`. Produces `out/kethosbase-plugin.wasm` + a patched Javy binary. |
| `build-patched-javy.sh` | Build ONLY the patched Javy CLI and emit `javy-patched-<JAVY_TAG>-<triple>.gz` + `.sha256` into a mounted `/out`. Used to seed the linux release legs locally. |
| `smoke.py` | Boot-and-run a compiled function module under wasmtime with stubbed `kethosbase` host imports + a request envelope; proves it doesn't trap on `_start` and returns a 2xx. |

## How the patched Javy reaches end users

`kethosbase functions deploy` downloads the patched Javy for the host platform on
first use (checksum-verified, cached). The binaries are published by
`.github/workflows/javy-patched.yml` on native runners to the GitHub Release
tagged **`javy-patched-v9.0.0`** (must equal `javytool.JavyVersion`). Asset names
are `javy-patched-v9.0.0-<rust-triple>.gz`, each with a `.sha256`.

### Platform matrix

| Platform | Rust triple | Built by |
|----------|-------------|----------|
| linux/amd64 | `x86_64-unknown-linux-gnu` | CI (`ubuntu-latest`) — seedable locally via Docker |
| linux/arm64 | `aarch64-unknown-linux-gnu` | CI (`ubuntu-24.04-arm`) — seedable locally via Docker+qemu |
| darwin/amd64 | `x86_64-apple-darwin` | CI (`macos-13`) |
| darwin/arm64 | `aarch64-apple-darwin` | CI (`macos-latest`) |
| windows/amd64 | `x86_64-pc-windows-msvc` | CI (`windows-latest`) |

### Seeding linux locally

```sh
docker run --rm --privileged tonistiigi/binfmt --install arm64   # once, for arm64
OUT=$(pwd)/dist; mkdir -p "$OUT"
docker run --rm --platform linux/amd64 \
  -v "$PWD/plugin/javy-toolchain:/patch" -v "$OUT:/out" \
  rust:1-bookworm bash /out/build-patched-javy.sh   # repeat with --platform linux/arm64
```

Upload `dist/*.gz` + `dist/*.gz.sha256` to the release, then copy each `.sha256`
into `javytool.patchedJavyAssets`.

## When bumping the Javy version

1. Update `javytool.JavyVersion` and `JAVY_TAG`/`RELEASE_TAG` in the workflow.
2. Re-run the workflow to publish new assets under the new tag.
3. Refresh every `patchedJavyAssets` checksum, and rebuild + re-embed the plugin
   (`internal/functions/javytool/build/plugin/kethosbase-plugin.wasm`).
