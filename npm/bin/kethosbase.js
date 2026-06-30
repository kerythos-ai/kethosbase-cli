#!/usr/bin/env node
// Launcher: exec the platform binary that install.js downloaded next to it,
// forwarding all arguments and the exit code.
const { spawnSync } = require("node:child_process");
const path = require("node:path");

const ext = process.platform === "win32" ? ".exe" : "";
const bin = path.join(__dirname, `kethosbase-bin${ext}`);

const result = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
if (result.error) {
  if (result.error.code === "ENOENT") {
    console.error(
      "kethosbase: binary not found — reinstall the package so postinstall can download it.",
    );
  } else {
    console.error("kethosbase: failed to run:", result.error.message);
  }
  process.exit(1);
}
process.exit(result.status ?? 1);
