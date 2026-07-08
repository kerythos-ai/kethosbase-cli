# Vendored Javy plugin artifact

`kethosbase-plugin.wasm` is the initialized Kethosbase Javy plugin, embedded into
the CLI via `go:embed` (see `../../plugin.go`). It is architecture-independent
Wasm — built once, used on every platform.

## Status: committed and working

Built from the Rust source at `/plugin` with a **patched** Javy (see
`/plugin/javy-toolchain`) and verified end to end: `kethosbase functions deploy
<serve-handler>.ts --dry-run` produces a real JS module whose imports are exactly
`{kethosbase, wasi_snapshot_preview1}` and which exports `_start`.

## Why a patched Javy is needed

Javy **v9.0.0** runs binaryen `wasm-opt` with the **MVP** feature set at *two*
call sites — plugin initialization (`plugin-processing`) and static `build`
(`codegen`). The WASI SDK's `libc` and QuickJS emit bulk-memory instructions
(`memory.copy`/`memory.fill`, non-zero active data-segment flags), which MVP
`wasm-opt` rejects:

```
[wasm-validator error] Bulk memory operations require bulk memory
  [--enable-bulk-memory], on (memory.fill ...)
```

The fix is a one-liner at each site: add `.all_features()` to the
`OptimizationOptions` chain (`.enable_feature(...)` alone does NOT relax the input
reader/validator — `all_features()` sets the baseline). `plugin/javy-toolchain`
applies this patch, builds the patched Javy in a Linux container, and produces
both this `.wasm` and the patched Javy binary.

Because the static `build` step also runs MVP `wasm-opt`, the **runtime** Javy the
CLI uses must be the patched build too (provide it via `KETHOSBASE_JAVY`, or a
Kethosbase-hosted patched release once published). Stock upstream Javy only works
for *dynamic* linking, which is not self-contained and would import the plugin
namespace rather than just `kethosbase` + wasi — so it cannot be deployed.

## Rebuilding

```sh
docker run --rm -v "$PWD:/repo" -w /repo rust:1-bookworm \
  bash plugin/javy-toolchain/build-javy-and-plugin.sh
cp plugin/javy-toolchain/out/kethosbase-plugin.wasm \
   internal/functions/javytool/build/plugin/kethosbase-plugin.wasm
```
