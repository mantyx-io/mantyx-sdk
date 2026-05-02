#!/usr/bin/env node
/**
 * Single source of truth: repo root VERSION.
 * Updates go/sdk-version.txt (Go embed), ts/package.json "version", and ts/src/version.ts.
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

function goEmbedContents(v) {
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
  const expectedGo = goEmbedContents(v);

  if (check) {
    if (pkg.version !== v) {
      console.error(`ts/package.json version ${pkg.version} != VERSION ${v}`);
      process.exit(1);
    }
    const cur = fs.readFileSync(tsVersionPath, "utf8");
    if (cur !== expectedTs) {
      console.error("ts/src/version.ts is out of sync; run: cd ts && npm run sync-version");
      process.exit(1);
    }
    const goCur = fs.readFileSync(goEmbedPath, "utf8");
    if (goCur !== expectedGo) {
      console.error(
        "go/sdk-version.txt is out of sync with repo VERSION; run: cd ts && npm run sync-version",
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
  console.log("synced Go embed, npm, and version.ts to", v);
}

main();
