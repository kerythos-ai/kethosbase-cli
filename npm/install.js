// postinstall: download the prebuilt kethosbase binary for this platform from
// the matching GitHub Release (v<version>) into ./bin. The npm package itself
// ships no binary — only this downloader and the launcher shim — so the package
// stays tiny and each user fetches just their platform's build.
const fs = require("node:fs");
const path = require("node:path");
const pkg = require("./package.json");

const PLATFORMS = { linux: "linux", darwin: "darwin", win32: "windows" };
const ARCHES = { x64: "amd64", arm64: "arm64" };

async function main() {
  if (process.env.KETHOSBASE_CLI_SKIP_DOWNLOAD) {
    return; // escape hatch for offline/CI setups that provide the binary another way
  }
  const os = PLATFORMS[process.platform];
  const arch = ARCHES[process.arch];
  if (!os || !arch) {
    throw new Error(`unsupported platform ${process.platform}/${process.arch}`);
  }
  const ext = process.platform === "win32" ? ".exe" : "";
  const asset = `kethosbase_${os}_${arch}${ext}`;
  const url = `https://github.com/kerythos-ai/kethosbase-cli/releases/download/v${pkg.version}/${asset}`;

  const destDir = path.join(__dirname, "bin");
  fs.mkdirSync(destDir, { recursive: true });
  const dest = path.join(destDir, `kethosbase-bin${ext}`);

  process.stdout.write(`kethosbase: downloading ${asset} (v${pkg.version})...\n`);
  const res = await fetch(url, { redirect: "follow" });
  if (!res.ok) {
    throw new Error(
      `download failed (HTTP ${res.status}) from ${url}\n` +
        `Ensure release v${pkg.version} exists and its assets are publicly downloadable.`,
    );
  }
  fs.writeFileSync(dest, Buffer.from(await res.arrayBuffer()));
  fs.chmodSync(dest, 0o755);
  process.stdout.write(`kethosbase: installed ${pkg.version}\n`);
}

main().catch((err) => {
  console.error("kethosbase install error:", err.message);
  process.exit(1);
});
