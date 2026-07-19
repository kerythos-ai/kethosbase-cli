#!/usr/bin/env python3
# Boot-and-run smoke for a Javy-compiled Kethosbase function module.
#
# Provides the `kethosbase` host imports (with the staging protocol) and WASI,
# feeds a request envelope on stdin, runs `_start`, and prints the response
# envelope written to stdout. A clean run proves the shim boots under QuickJS
# (no TextEncoder-at-boot trap) AND that a db.query round-trip returns a 2xx.
import sys, json, base64
from wasmtime import (
    Engine, Store, Module, Linker, WasiConfig, FuncType, ValType, Func,
)

wasm_path = sys.argv[1]

# The request envelope fed to the module on stdin.
request = {
    "method": "GET",
    "path": "/hello",
    "headers": {"x-smoke": "1"},
}
stdin_bytes = json.dumps(request).encode("utf-8")

engine = Engine()
store = Store(engine)
module = Module.from_file(engine, wasm_path)

# WASI: stdin = request envelope, stdout/stderr captured to files.
import tempfile, os
out_path = tempfile.mktemp()
err_path = tempfile.mktemp()
in_path = tempfile.mktemp()
with open(in_path, "wb") as f:
    f.write(stdin_bytes)

wasi = WasiConfig()
wasi.stdin_file = in_path
wasi.stdout_file = out_path
wasi.stderr_file = err_path
store.set_wasi(wasi)

linker = Linker(engine)
linker.define_wasi()

# Single staged buffer (the module drains it via kb_read).
staged = {"buf": b"", "off": 0}
# Filled in after instantiation; host funcs close over this holder.
mem_holder = {"mem": None}

def read_str(ptr, length):
    m = mem_holder["mem"]
    data = m.read(store, ptr, ptr + length)
    return bytes(data)

def stage(obj):
    staged["buf"] = json.dumps(obj).encode("utf-8")
    staged["off"] = 0
    return len(staged["buf"])

i32 = ValType.i32()

def kb_log(ptr, length):
    msg = read_str(ptr, length).decode("utf-8", "replace")
    sys.stderr.write("[kb_log] " + msg + "\n")

def kb_db_query(ptr, length):
    req = json.loads(read_str(ptr, length))
    sys.stderr.write("[kb_db_query] " + json.dumps(req) + "\n")
    # Return a fake row so serve()'s handler can build a 200.
    return stage({"rows": [{"now": "2026-07-07T00:00:00Z"}]})

def kb_fetch(ptr, length):
    return stage({"status": 200, "headers": {}, "body": base64.b64encode(b"ok").decode()})

def kb_get_secret(ptr, length):
    return stage({"value": "smoke-secret"})

def kb_sign(ptr, length):
    req = json.loads(read_str(ptr, length))
    sys.stderr.write("[kb_sign] alg=" + str(req.get("alg")) + " key=" + str(req.get("key")) + "\n")
    # A stand-in signature: the real host signs in host memory with the PEM held
    # by the named secret. 64 bytes matches an ES256 r||s signature.
    return stage({"signature": base64.b64encode(b"\x01" * 64).decode()})

def kb_read(dst, length):
    m = mem_holder["mem"]
    remaining = staged["buf"][staged["off"]:]
    n = min(len(remaining), length)
    if n:
        m.write(store, remaining[:n], dst)
        staged["off"] += n
    return n

linker.define_func("kethosbase", "kb_log", FuncType([i32, i32], []), kb_log)
linker.define_func("kethosbase", "kb_db_query", FuncType([i32, i32], [i32]), kb_db_query)
linker.define_func("kethosbase", "kb_fetch", FuncType([i32, i32], [i32]), kb_fetch)
linker.define_func("kethosbase", "kb_get_secret", FuncType([i32, i32], [i32]), kb_get_secret)
linker.define_func("kethosbase", "kb_sign", FuncType([i32, i32], [i32]), kb_sign)
linker.define_func("kethosbase", "kb_read", FuncType([i32, i32], [i32]), kb_read)
# Alias export kb_db_read has identical behavior.
linker.define_func("kethosbase", "kb_db_read", FuncType([i32, i32], [i32]), kb_read)

instance = linker.instantiate(store, module)

# Resolve the module's exported memory and hand it to the host funcs.
exports = instance.exports(store)
mem_holder["mem"] = exports.get("memory")
if mem_holder["mem"] is None:
    print("FAIL: module has no exported memory", file=sys.stderr)
    sys.exit(2)

start = exports.get("_start")
if start is None:
    print("FAIL: no _start export", file=sys.stderr)
    sys.exit(2)

trapped = False
try:
    start(store)
except Exception as e:
    trapped = True
    sys.stderr.write("TRAP during _start: " + repr(e) + "\n")

out = open(out_path, "rb").read()
err = open(err_path, "rb").read().decode("utf-8", "replace")
sys.stderr.write("---- module stderr ----\n" + err + "\n----\n")

if trapped:
    print("SMOKE FAIL: module trapped during _start")
    sys.exit(1)

print("stdout (response envelope):", out.decode("utf-8", "replace"))
try:
    env = json.loads(out)
except Exception as e:
    print("SMOKE FAIL: response is not valid JSON:", e)
    sys.exit(1)

status = env.get("status")
if isinstance(status, int) and 200 <= status < 300:
    body = env.get("body")
    if body:
        body = base64.b64decode(body).decode("utf-8", "replace")
    print(f"SMOKE OK: status={status} body={body}")
    sys.exit(0)
print(f"SMOKE FAIL: unexpected status {status}")
sys.exit(1)
