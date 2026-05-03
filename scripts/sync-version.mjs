#!/usr/bin/env node
/**
 * Single source of truth: repo root VERSION.
 * Updates:
 *   - go/sdk-version.txt           (Go //go:embed)
 *   - ts/package.json "version"    (npm)
 *   - ts/src/version.ts            (TS const SDK_VERSION)
 *   - python/sdk-version.txt       (parity with Go)
 *   - python/src/mantyx/_version.py (hatchling reads this for `pip install`)
 *
 * Usage: node scripts/sync-version.mjs [--check]
 */
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const root = path.join(__dirname, "..");
const versionFile = path.join(root, "VERSION");
const goEmbedPath = path.join(root, "go", "sdk-version.txt");
const pkgPath = path.join(root, "ts", "package.json");
const tsVersionPath = path.join(root, "ts", "src", "version.ts");
const pyEmbedPath = path.join(root, "python", "sdk-version.txt");
const pyVersionPath = path.join(root, "python", "src", "mantyx", "_version.py");

function readRootVersion() {
  return fs.readFileSync(versionFile, "utf8").trim();
}

function tsVersionSource(v) {
  return (
    "/**\n" +
    " * Release version — synced from repo root VERSION (`npm run sync-version`).\n" +
    " */\n" +
    `export const SDK_VERSION = ${JSON.stringify(v)};\n`
  );
}

function pyVersionSource(v) {
  return (
    `"""Release version — synced from repo root VERSION (\`node scripts/sync-version.mjs\`)."""\n\n` +
    `__version__ = ${JSON.stringify(v)}\n\n` +
    `SDK_VERSION = __version__\n`
  );
}

function plainVersion(v) {
  return `${v}\n`;
}

function main() {
  const v = readRootVersion();
  if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$/.test(v)) {
    console.error("VERSION must be a semver string, got:", JSON.stringify(v));
    process.exit(1);
  }

  const check = process.argv.includes("--check");
  const pkg = JSON.parse(fs.readFileSync(pkgPath, "utf8"));
  const expectedTs = tsVersionSource(v);
  const expectedGo = plainVersion(v);
  const expectedPy = pyVersionSource(v);
  const expectedPyEmbed = plainVersion(v);

  if (check) {
    if (pkg.version !== v) {
      console.error(`ts/package.json version ${pkg.version} != VERSION ${v}`);
      process.exit(1);
    }
    const cur = fs.readFileSync(tsVersionPath, "utf8");
    if (cur !== expectedTs) {
      console.error("ts/src/version.ts is out of sync; run: node scripts/sync-version.mjs");
      process.exit(1);
    }
    const goCur = fs.readFileSync(goEmbedPath, "utf8");
    if (goCur !== expectedGo) {
      console.error(
        "go/sdk-version.txt is out of sync with repo VERSION; run: node scripts/sync-version.mjs",
      );
      process.exit(1);
    }
    if (!fs.existsSync(pyVersionPath)) {
      console.error(
        `python/src/mantyx/_version.py does not exist; run: node scripts/sync-version.mjs`,
      );
      process.exit(1);
    }
    const pyCur = fs.readFileSync(pyVersionPath, "utf8");
    if (pyCur !== expectedPy) {
      console.error(
        "python/src/mantyx/_version.py is out of sync; run: node scripts/sync-version.mjs",
      );
      process.exit(1);
    }
    if (!fs.existsSync(pyEmbedPath)) {
      console.error(
        `python/sdk-version.txt does not exist; run: node scripts/sync-version.mjs`,
      );
      process.exit(1);
    }
    const pyEmbedCur = fs.readFileSync(pyEmbedPath, "utf8");
    if (pyEmbedCur !== expectedPyEmbed) {
      console.error(
        "python/sdk-version.txt is out of sync with repo VERSION; run: node scripts/sync-version.mjs",
      );
      process.exit(1);
    }
    console.log("version sync OK:", v);
    return;
  }

  pkg.version = v;
  fs.writeFileSync(pkgPath, `${JSON.stringify(pkg, null, 2)}\n`);
  fs.writeFileSync(tsVersionPath, expectedTs);
  fs.writeFileSync(goEmbedPath, expectedGo);
  fs.mkdirSync(path.dirname(pyVersionPath), { recursive: true });
  fs.writeFileSync(pyVersionPath, expectedPy);
  fs.writeFileSync(pyEmbedPath, expectedPyEmbed);
  console.log("synced Go embed, npm, version.ts, and Python to", v);
}

main();
