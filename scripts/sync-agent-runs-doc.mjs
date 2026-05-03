#!/usr/bin/env node
/**
 * Single source of truth for the wire protocol spec: docs/agent-runs-protocol.md
 *
 * Identical copies live under each SDK tree so published packages and GitHub
 * browsing always include the spec next to the client code.
 *
 * Usage:
 *   node scripts/sync-agent-runs-doc.mjs           # copy canonical → mirrors
 *   node scripts/sync-agent-runs-doc.mjs --check   # verify mirrors match (CI)
 */
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, "..");
const canonical = path.join(repoRoot, "docs", "agent-runs-protocol.md");

const mirrors = [
  path.join(repoRoot, "go", "docs", "agent-runs-protocol.md"),
  path.join(repoRoot, "python", "docs", "agent-runs-protocol.md"),
  path.join(repoRoot, "ts", "docs", "agent-runs-protocol.md"),
];

function main() {
  const check = process.argv.includes("--check");

  if (!fs.existsSync(canonical)) {
    console.error(`missing canonical file: ${path.relative(repoRoot, canonical)}`);
    process.exit(1);
  }

  const src = fs.readFileSync(canonical);

  if (check) {
    let ok = true;
    for (const dest of mirrors) {
      if (!fs.existsSync(dest)) {
        console.error(`missing mirror: ${path.relative(repoRoot, dest)}`);
        ok = false;
        continue;
      }
      const destBuf = fs.readFileSync(dest);
      if (!src.equals(destBuf)) {
        console.error(`out of sync (run: node scripts/sync-agent-runs-doc.mjs): ${path.relative(repoRoot, dest)}`);
        ok = false;
      }
    }
    if (!ok) process.exit(1);
    console.log("sync-agent-runs-doc: mirrors match docs/agent-runs-protocol.md");
    return;
  }

  for (const dest of mirrors) {
    fs.mkdirSync(path.dirname(dest), { recursive: true });
    fs.writeFileSync(dest, src);
    console.log(`sync-agent-runs-doc: wrote ${path.relative(repoRoot, dest)}`);
  }
}

main();
