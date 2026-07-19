# Kethosbase Javy plugin

A [Javy](https://github.com/bytecodealliance/javy) plugin (QuickJS-on-Wasm) that
installs the `__kb_*` global functions the `@kethosbase/functions` SDK calls, and
forwards them to the platform's `kethosbase` host functions. This realizes the
CLI/toolchain half of **ADR-0091** (JS-on-WASM Functions).

## Why a plugin?

A Javy-compiled module runs QuickJS in Wasm and, by default, can only reach
stdio. To let the JS reach *our* host functions we ship a custom Javy plugin
that (1) imports the platform host functions from Wasm module namespace
`kethosbase` and (2) exposes them to the QuickJS runtime as globals.

## Pinned versions

| Tool | Version |
|------|---------|
| Javy CLI | **v9.0.0** |
| `javy-plugin-api` | **v7.0.0** |
| Rust target | `wasm32-wasip1` |

WASI **preview 1** is used deliberately: the plugin owns the module's stdin/stdout
(the request/response envelope travels over WASI stdio), which preview-1 plugins
can do directly. See the Javy "extending" docs for the preview-1 plugin recipe.

The `import_namespace!("kethosbase_javy_v1")` value must change whenever the Javy
default plugin's QuickJS bytecode format changes (it did in Javy v8 and v9), so a
generated module can never be linked against a mismatched plugin.

## The shared bridge contract

The plugin installs exactly these globals (names/shapes are load-bearing — the
SDK depends on them verbatim):

| Global | Signature | Backed by |
|--------|-----------|-----------|
| `__kb_read_request()` | `(): string` | reads all of stdin |
| `__kb_write_response(json)` | `(string): void` | writes stdout |
| `__kb_log(msg)` | `(string): void` | host `kb_log` |
| `__kb_db_query(reqJson)` | `(string): string` | host `kb_db_query` + `kb_read` |
| `__kb_fetch(reqJson)` | `(string): string` | host `kb_fetch` + `kb_read` |
| `__kb_get_secret(name)` | `(string): string` | host `kb_get_secret` + `kb_read` |
| `__kb_sign(reqJson)` | `(string): string` | host `kb_sign` + `kb_read` |

Producers stage a result and return its length; the plugin then drains it with
`kb_read` and returns the JSON string to JS. Host framing errors (`-1`) are
converted to a `{"error":"..."}` JSON string so JS always gets valid JSON.

`__kb_sign` (ADR-0126) takes `{"alg":"RS256"|"ES256","key":"<secret name>",
"data":"<base64>"}` and stages `{"signature":"<base64>"}` or `{"error":"..."}`.
Note `key` is the **name** of a Function secret holding a PEM private key, never
key material: the host unseals the secret and signs in host memory, so the
private key never enters this module's linear memory. The plugin must not grow a
key-by-value path.

**Deployment ordering:** the `kethosbase.kb_sign` import is emitted
unconditionally by every module built with this plugin, and wazero refuses to
instantiate a module with an unsatisfied import. A host without `kb_sign` would
therefore fail *every* JS function, not just signing ones. Ship the host side
first, then this artifact.

## Building the vendored plugin artifact

The initialized plugin `.wasm` is a build artifact committed under
`../internal/functions/javytool/build/plugin/kethosbase-plugin.wasm` (embedded
via `go:embed`) so `functions deploy` never needs a Rust toolchain at runtime.

It must be built with a **patched Javy** (Javy v9.0.0's `wasm-opt` runs with the
MVP feature set and rejects the bulk-memory ops in wasi-libc/QuickJS). The
reproducible container recipe in `javy-toolchain/` clones Javy, applies the
one-line `.all_features()` patch to both `wasm-opt` call sites, builds the
patched Javy, and runs `init-plugin` — no local Rust/Javy needed, just Docker:

```sh
# From the repo root:
docker run --rm -v "$PWD:/repo" -w /repo rust:1-bookworm \
  bash plugin/javy-toolchain/build-javy-and-plugin.sh
cp plugin/javy-toolchain/out/kethosbase-plugin.wasm \
   internal/functions/javytool/build/plugin/kethosbase-plugin.wasm
```

`build.sh` (local; requires rustup + a **patched** javy on PATH) is the
non-container equivalent. See
`../internal/functions/javytool/build/plugin/README.md` for the full rationale
and the patched-Javy runtime requirement.
