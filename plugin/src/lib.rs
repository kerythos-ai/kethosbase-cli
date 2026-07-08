//! Kethosbase Javy plugin (WASI preview 1).
//!
//! A Javy plugin embeds a QuickJS runtime into a Wasm module. By default that
//! runtime can only touch stdio (via `Javy.IO`). This plugin additionally
//! installs six global functions into the JS runtime — the *shared bridge
//! contract* the `@kethosbase/functions` SDK (kethosbase-js repo) depends on:
//!
//!   __kb_read_request(): string             — read the request envelope JSON
//!   __kb_write_response(json: string): void — write the response envelope JSON
//!   __kb_log(msg: string): void
//!   __kb_db_query(reqJson: string): string  — returns staged result JSON
//!   __kb_fetch(reqJson: string): string     — returns staged result JSON
//!   __kb_get_secret(name: string): string   — returns staged result JSON
//!
//! `__kb_read_request` / `__kb_write_response` own the module's stdin/stdout so
//! the JS never touches raw WASI stdio directly. The other four forward to the
//! platform host functions imported from the Wasm module namespace `kethosbase`
//! (host ABI defined by ADR-0084 / ADR-0089), marshalling strings through the
//! plugin's own linear memory and following the one-buffer staging protocol.
//!
//! Because a Javy-generated module dynamically links against this plugin and
//! shares its linear memory, the `(ptr,len)` pairs we hand the host point into
//! the same memory the host reads/writes — so passing the address of a Rust
//! `Vec<u8>`/`String` is correct.

use std::io::{self, Read, Write};

use javy_plugin_api::javy::quickjs::prelude::Func;
use javy_plugin_api::{import_namespace, Config};

// Import namespace for the dynamically linked modules this plugin builds.
// A generated module will import the QuickJS provider from this namespace.
// Keep it distinct from the platform host namespace ("kethosbase").
import_namespace!("kethosbase_javy_v1");

// ---- platform host functions (Wasm module namespace "kethosbase") ----
//
// These are the exact imports the platform's deploy-time validator allows and
// the runtime provides. Every parameter/result lowers to i32 under wasip1.
#[link(wasm_import_module = "kethosbase")]
extern "C" {
    // kb_log(ptr, len) -> ()
    fn kb_log(ptr: *const u8, len: u32);
    // Producers: read a JSON request from [ptr,len), STAGE a result, return the
    // staged length (>=0) or -1 on a host framing error.
    fn kb_db_query(ptr: *const u8, len: u32) -> i32;
    fn kb_fetch(ptr: *const u8, len: u32) -> i32;
    fn kb_get_secret(ptr: *const u8, len: u32) -> i32;
    // kb_read(dst, len) -> n: drain up to len staged bytes into [dst,len),
    // returning bytes copied (may chunk over calls) or -1.
    fn kb_read(dst: *mut u8, len: u32) -> i32;
}

/// Drain exactly `n` staged bytes into a fresh buffer using `kb_read`. The host
/// may return fewer than requested per call, so loop until drained.
fn drain_staged(n: i32) -> Result<Vec<u8>, String> {
    if n < 0 {
        return Err("host framing error".to_string());
    }
    let total = n as usize;
    let mut buf = vec![0u8; total];
    let mut filled = 0usize;
    while filled < total {
        let got = unsafe { kb_read(buf[filled..].as_mut_ptr(), (total - filled) as u32) };
        if got < 0 {
            return Err("kb_read failed".to_string());
        }
        if got == 0 {
            break;
        }
        filled += got as usize;
    }
    buf.truncate(filled);
    Ok(buf)
}

/// Call a producer host function with `req` bytes, then drain and return the
/// staged JSON as a String. On a host framing error, synthesize a JSON error
/// object so the JS side always receives valid JSON.
fn call_producer(producer: unsafe extern "C" fn(*const u8, u32) -> i32, req: &str) -> String {
    let bytes = req.as_bytes();
    let n = unsafe { producer(bytes.as_ptr(), bytes.len() as u32) };
    match drain_staged(n) {
        Ok(out) => String::from_utf8_lossy(&out).into_owned(),
        Err(e) => format!("{{\"error\":{}}}", json_string(&e)),
    }
}

/// Minimal JSON string encoder (avoids pulling serde into the plugin for a
/// single error path).
fn json_string(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// Build the QuickJS runtime config. We enable the event loop so `async`
/// handlers work: `serve(async (req) => ...)` returns a promise, and without the
/// event loop QuickJS reports "Pending jobs in the event queue" and traps after
/// `_start`. `text_encoding` is enabled as a convenience (the shim is pure-JS and
/// does not depend on it, but user code may reference TextEncoder/TextDecoder).
fn kb_config() -> Config {
    let mut config = Config::default();
    config.event_loop(true).text_encoding(true);
    config
}

#[export_name = "initialize-runtime"]
pub extern "C" fn initialize_runtime() {
    javy_plugin_api::initialize_runtime(kb_config, |runtime| {
        runtime.context().with(|ctx| {
            let globals = ctx.globals();

            // Read the whole request envelope from stdin. The plugin owns stdio;
            // the JS runtime never reads raw stdin itself.
            globals
                .set(
                    "__kb_read_request",
                    Func::from(|| -> String {
                        let mut buf = Vec::new();
                        // Ignore read errors: an empty request yields "" and the
                        // SDK surfaces a clear error.
                        let _ = io::stdin().read_to_end(&mut buf);
                        String::from_utf8_lossy(&buf).into_owned()
                    }),
                )
                .unwrap();

            // Write the response envelope to stdout.
            globals
                .set(
                    "__kb_write_response",
                    Func::from(|json: String| {
                        let mut out = io::stdout();
                        let _ = out.write_all(json.as_bytes());
                        let _ = out.flush();
                    }),
                )
                .unwrap();

            globals
                .set(
                    "__kb_log",
                    Func::from(|msg: String| {
                        let bytes = msg.as_bytes();
                        unsafe { kb_log(bytes.as_ptr(), bytes.len() as u32) };
                    }),
                )
                .unwrap();

            globals
                .set(
                    "__kb_db_query",
                    Func::from(|req: String| -> String { call_producer(kb_db_query, &req) }),
                )
                .unwrap();

            globals
                .set(
                    "__kb_fetch",
                    Func::from(|req: String| -> String { call_producer(kb_fetch, &req) }),
                )
                .unwrap();

            globals
                .set(
                    "__kb_get_secret",
                    Func::from(|name: String| -> String { call_producer(kb_get_secret, &name) }),
                )
                .unwrap();
        });
        runtime
    })
    .unwrap();
}
