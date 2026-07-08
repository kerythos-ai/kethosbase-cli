# Vendored Javy plugin artifact

`kethosbase-plugin.wasm` is the initialized Kethosbase Javy plugin, embedded into
the CLI via `go:embed` (see `../../plugin.go`). It is built from the Rust source
at `/plugin` by `/plugin/build.sh`:

```
cargo build --target=wasm32-wasip1 --release   # -> kethosbase_javy_plugin.wasm
javy init-plugin <that> -o kethosbase-plugin.wasm
```

## Current status: artifact NOT yet committed (placeholder is empty)

The Rust plugin **compiles cleanly** to `wasm32-wasip1`, but Javy **v9.0.0's**
`init-plugin` step currently rejects it: its embedded `wasm-opt` runs with the
MVP feature set and errors on the `memory.fill` (bulk-memory) instructions that
the WASI SDK's `libc`/QuickJS objects contain:

```
[wasm-validator error] Bulk memory operations require bulk memory
  [--enable-bulk-memory], on (memory.fill ...)
```

This is an upstream Javy limitation, not a defect in the plugin: `init-plugin`
(`crates/plugin-processing/src/lib.rs::optimize_module`) calls
`wasm_opt::OptimizationOptions::new_opt_level_3()` **without** enabling
`Feature::BulkMemory`, so it cannot initialize any plugin whose linked C/Rust
objects emit bulk-memory ops (which is essentially all of them).

### Options to unblock (pick one, then run build.sh and commit the .wasm)

1. **Patched Javy for init only** — build the Javy CLI from source with
   `optimize_module` enabling `BulkMemory` (+ `SignExt`, `MutableGlobals`,
   `Multivalue`, `TruncSat`, `ReferenceTypes`), use it solely for the one-time
   `init-plugin` step, and file the fix upstream. The runtime/build javy on
   users' machines is unaffected — they only run `javy build`, which is fine.
2. **Wait for an upstream fix** and bump `JavyVersion` once released.
3. **WASI preview 2 plugin** variant (no wasi-libc bulk-memory from stdio) — more
   work; the preview-1 design here was chosen because the plugin owns stdio.

Until the artifact is committed, `kethosbase functions deploy` bundles and
validates correctly but the Javy compile step returns a clear error pointing at
`plugin/build.sh`. The rest of the pipeline (bundling, wasm validation, upload)
is exercised by the Go tests, including against a real `kethosbase`-importing
module fixture.
