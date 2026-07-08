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

Producers stage a result and return its length; the plugin then drains it with
`kb_read` and returns the JSON string to JS. Host framing errors (`-1`) are
converted to a `{"error":"..."}` JSON string so JS always gets valid JSON.

## Building the vendored plugin artifact

The initialized plugin `.wasm` is a build artifact committed under
`../internal/functions/javytool/build/plugin/kethosbase-plugin.wasm` (embedded
via `go:embed`) so `functions deploy` never needs a Rust toolchain at runtime.
Rebuild it only when this source or the pinned Javy version changes:

```sh
# Requires: rustup + `rustup target add wasm32-wasip1`, and the pinned javy CLI.
./build.sh
```

`build.sh` runs `cargo build --target=wasm32-wasip1 --release` and then
`javy init-plugin` (the required validation/initialization step) to produce the
final vendored `.wasm`, then writes its `.sha256` alongside.
