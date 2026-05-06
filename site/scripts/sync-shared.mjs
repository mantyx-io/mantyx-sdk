#!/usr/bin/env node
/**
 * Mirror external markdown files (e.g. the wire-protocol spec, top-level
 * READMEs) into the Starlight content collection. The targets are
 * gitignored — they're regenerated on every `npm run dev` and `npm run
 * build` so the source of truth stays in the repo root.
 */
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const siteRoot = path.resolve(here, "..");
const repoRoot = path.resolve(siteRoot, "..");
const docsRoot = path.join(siteRoot, "src", "content", "docs", "docs");

/**
 * Strip the first H1 heading from a markdown body so Starlight uses the
 * frontmatter `title` for the page heading instead.
 */
function stripFirstH1(md) {
  return md.replace(/^# [^\n]+\n+/, "");
}

/**
 * Rewrite intra-repo links so they resolve correctly when rendered under
 * `/docs/protocol/` rather than at the repo root.
 */
function rewriteLinks(md) {
  return md
    // Drop ./agent-runs.md companion link (server-side doc not on the public site).
    .replace(/\(\.\/agent-runs\.md\)/g, "(https://github.com/mantyx-io/mantyx-sdk/blob/main/docs/agent-runs-protocol.md#companion-doc)")
    // Cross-link the two protocol documents so they resolve on the live site.
    .replace(/\(\.\/wire-protocol\.md\)/g, "(/docs/wire-protocol/)")
    .replace(/\[`wire-protocol\.md`\]\(\.\/wire-protocol\.md\)/g, "[Wire protocol — messaging & data structures](/docs/wire-protocol/)")
    .replace(/\(\.\/agent-runs-protocol\.md\)/g, "(/docs/protocol/)")
    .replace(/\[`agent-runs-protocol\.md`\]\(\.\/agent-runs-protocol\.md\)/g, "[Agent-runs protocol](/docs/protocol/)")
    // Rewrite SDK package directory references to the public site routes.
    .replace(/\.\.\/packages\/mantyx-sdk\/ts\//g, "/docs/reference/typescript/")
    .replace(/\.\.\/packages\/mantyx-sdk\/go\//g, "/docs/reference/go/");
}

function writeWithFrontmatter(targetRel, frontmatter, body) {
  const target = path.join(docsRoot, targetRel);
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.writeFileSync(
    target,
    `---\n${Object.entries(frontmatter)
      .map(([k, v]) => `${k}: ${typeof v === "string" ? JSON.stringify(v) : JSON.stringify(v)}`)
      .join("\n")}\n---\n\n${body}\n`,
  );
  console.log(`[sync-shared] wrote ${path.relative(siteRoot, target)}`);
}

function syncProtocol() {
  const src = path.join(repoRoot, "docs", "agent-runs-protocol.md");
  const md = stripFirstH1(rewriteLinks(fs.readFileSync(src, "utf8")));
  writeWithFrontmatter(
    "protocol.md",
    {
      title: "Agent-runs protocol",
      description:
        "HTTP routes, auth, body shapes, and session semantics of the MANTYX agent-runs API.",
    },
    md,
  );
}

function syncWireProtocol() {
  const src = path.join(repoRoot, "docs", "wire-protocol.md");
  if (!fs.existsSync(src)) {
    console.warn(`[sync-shared] source missing: docs/wire-protocol.md (skipped)`);
    return;
  }
  const md = stripFirstH1(rewriteLinks(fs.readFileSync(src, "utf8")));
  writeWithFrontmatter(
    "wire-protocol.md",
    {
      title: "Wire protocol — messaging & data structures",
      description:
        "Every SSE event and resolved-content blob exchanged between MANTYX and an SDK during an agent run.",
    },
    md,
  );
}

function syncReference(slug, srcRel, title) {
  const src = path.join(repoRoot, srcRel);
  if (!fs.existsSync(src)) {
    console.warn(`[sync-shared] source missing: ${srcRel} (skipped)`);
    return;
  }
  const md = stripFirstH1(fs.readFileSync(src, "utf8"));
  writeWithFrontmatter(
    `reference/${slug}.md`,
    {
      title,
      description: `Reference for the ${title} client.`,
    },
    md,
  );
}

function main() {
  fs.mkdirSync(path.join(docsRoot, "reference"), { recursive: true });
  syncProtocol();
  syncWireProtocol();
  syncReference("typescript", "ts/README.md", "TypeScript SDK (@mantyx/sdk)");
  syncReference("go", "go/README.md", "Go SDK (mantyx-go-sdk)");
  syncReference("python", "python/README.md", "Python SDK (mantyx-sdk)");
}

main();
