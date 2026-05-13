#!/usr/bin/env node
/**
 * Single source of truth for the wire-protocol docs:
 *
 *   - docs/agent-runs-protocol.md   (HTTP routes, auth, body shapes)
 *   - docs/wire-protocol.md         (SSE events, tool-call envelopes,
 *                                    resolved data structures)
 *   - docs/oauth.md                 (OAuth 2.0 grants, tokens, revoke,
 *                                    token lifetimes)
 *
 * Identical copies live under each SDK tree so published packages and
 * GitHub browsing always include the specs next to the client code.
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
const sdkDirs = ["go", "python", "ts"];

// Each doc is mirrored into <sdk>/docs/<basename>.
const docs = [
  { name: "agent-runs-protocol.md" },
  { name: "wire-protocol.md" },
  { name: "oauth.md" },
];

function canonicalPath(doc) {
  return path.join(repoRoot, "docs", doc.name);
}

function mirrorPath(doc, sdk) {
  return path.join(repoRoot, sdk, "docs", doc.name);
}

function main() {
  const check = process.argv.includes("--check");

  for (const doc of docs) {
    if (!fs.existsSync(canonicalPath(doc))) {
      console.error(`missing canonical file: ${path.relative(repoRoot, canonicalPath(doc))}`);
      process.exit(1);
    }
  }

  if (check) {
    let ok = true;
    for (const doc of docs) {
      const src = fs.readFileSync(canonicalPath(doc));
      for (const sdk of sdkDirs) {
        const dest = mirrorPath(doc, sdk);
        if (!fs.existsSync(dest)) {
          console.error(`missing mirror: ${path.relative(repoRoot, dest)}`);
          ok = false;
          continue;
        }
        const destBuf = fs.readFileSync(dest);
        if (!src.equals(destBuf)) {
          console.error(
            `out of sync (run: node scripts/sync-agent-runs-doc.mjs): ${path.relative(repoRoot, dest)}`,
          );
          ok = false;
        }
      }
    }
    if (!ok) process.exit(1);
    console.log("sync-agent-runs-doc: mirrors match docs/*.md");
    return;
  }

  for (const doc of docs) {
    const src = fs.readFileSync(canonicalPath(doc));
    for (const sdk of sdkDirs) {
      const dest = mirrorPath(doc, sdk);
      fs.mkdirSync(path.dirname(dest), { recursive: true });
      fs.writeFileSync(dest, src);
      console.log(`sync-agent-runs-doc: wrote ${path.relative(repoRoot, dest)}`);
    }
  }
}

main();
