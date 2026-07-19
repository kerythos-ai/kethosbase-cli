# Vendored Javy plugin artifact

`kethosbase-plugin.wasm` is the initialized Kethosbase Javy plugin, embedded into
the CLI via `go:embed` (see `../../plugin.go`). It is architecture-independent
Wasm — built once, used on every platform.

## Status: committed and working

Built from the Rust source at `/plugin` with a **patched** Javy (see
`/plugin/javy-toolchain`) and verified end to end: a real `serve()` handler that
calls `db.query` + `log` boots under QuickJS, reaches the DB query, and returns a
200 JSON response. Its imports are exactly `{kethosbase, wasi_snapshot_preview1}`
and it exports `_start`. See `/plugin/javy-toolchain/smoke.py` for the
boot-and-run smoke (stubs the host imports and feeds a request envelope).

Two runtime requirements are baked into this plugin (`plugin/src/lib.rs`):

- **Event loop enabled** (`config.event_loop(true)`): `serve()` takes an `async`
  handler, so a promise is pending after `_start`; without the event loop QuickJS
  traps with "Pending jobs in the event queue".
- **QuickJS-safe SDK**: QuickJS provides no `TextEncoder`/`TextDecoder`/`atob`/
  `btoa`/`Buffer`/`crypto`. The embedded `sdk_shim.js` implements UTF-8 + base64
  in pure JS (a Go test enforces this). `text_encoding(true)` is also enabled so
  user code that references `TextEncoder`/`TextDecoder` still works. Note the SDK
  *exports* its own `crypto` (a partial `crypto.subtle.sign` routed through
  `__kb_sign`, ADR-0126) exactly as it exports its own `fetch` — that is a shim
  definition, not a read of the absent QuickJS global.

## Import-set changes are breaking

Every module built with this plugin imports the full `kethosbase` set the plugin
declares, whether or not the JS uses it — including `kb_sign` as of ADR-0126.
wazero refuses to instantiate a module with an unsatisfied import, so a plugin
that imports a host function the platform does not export breaks **every** JS
function, not just the ones using it. Always ship the host side first, then this
artifact.

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
