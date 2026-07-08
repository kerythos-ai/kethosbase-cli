#!/usr/bin/env python3
# Patch every binaryen wasm-opt `OptimizationOptions` call site in a Javy source
# tree to enable all post-MVP wasm features, so `init-plugin` AND static `build`
# accept modules containing bulk-memory (memory.copy/fill, active-segment flags)
# emitted by wasi-libc / QuickJS. Idempotent. Usage: patch-javy.py <javy-src-dir>
import sys, re, pathlib

root = pathlib.Path(sys.argv[1])
files = [
    root / "crates/plugin-processing/src/lib.rs",  # init-plugin path
    root / "crates/codegen/src/lib.rs",            # static build path
]
patched = False
for p in files:
    s = p.read_text(encoding="utf-8")
    if ".all_features()" in s:
        print(f"   {p.name}: already patched")
        patched = True
        continue
    # Insert `.all_features()` right after each `.debug_info(false)`, matching
    # the existing indentation.
    new, n = re.subn(
        r"([ \t]*)\.debug_info\(false\)",
        lambda m: f"{m.group(1)}.debug_info(false)\n{m.group(1)}.all_features()",
        s,
    )
    if n == 0:
        sys.exit(f"ERROR: wasm-opt anchor not found in {p}")
    p.write_text(new, encoding="utf-8")
    print(f"   {p.name}: patched {n} site(s)")
    patched = True
if not patched:
    sys.exit("ERROR: nothing patched")
