#!/usr/bin/env node
/**
 * Driver for git-cliff that regenerates per-SDK CHANGELOG.md files from the
 * Conventional Commits history. Drives a single `cliff.toml` with a different
 * `--include-path` (and `--tag-pattern`) per package so the TypeScript SDK's
 * CHANGELOG only contains TypeScript-relevant commits, etc.
 *
 *   node scripts/changelog.mjs --check
 *     Exits non-zero if any CHANGELOG.md is out of sync. Used in CI.
 *
 *   node scripts/changelog.mjs --write
 *     Regenerates ts/CHANGELOG.md, go/CHANGELOG.md, python/CHANGELOG.md
 *     in place. The existing `[Unreleased]` and previously-released
 *     sections in each file are *replaced* by the rendered output, since
 *     git-cliff is the source of truth.
 *
 *   node scripts/changelog.mjs --release-body --version=X.Y.Z [--package=ts|go|py]
 *     Prints just the unreleased section to stdout, suitable for piping into
 *     `gh release create --notes-file -`. Defaults to a unified release body
 *     spanning all three SDKs when --package is omitted.
 *
 * Requires `git-cliff` on PATH. Install via:
 *   brew install git-cliff       (macOS)
 *   cargo install git-cliff      (Rust)
 *   GitHub Actions: taiki-e/install-action with tool=git-cliff
 */
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, "..");

/**
 * Per-package config — paths whose commits should be included in the
 * package's CHANGELOG, plus the git tag prefix git-cliff should follow.
 *
 * `tagPattern` matches a glob over `refs/tags/`; the leading prefix is what
 * `publish.yml` pushes (`v<version>` for TS / repo, `go/v<version>` for Go,
 * `python/v<version>` for Python).
 */
const PACKAGES = {
  ts: {
    label: "TypeScript SDK (@mantyx/sdk)",
    changelog: "ts/CHANGELOG.md",
    includePaths: ["ts/**", "docs/**", "VERSION"],
    tagPattern: "v[0-9]*",
    repoLink: "https://github.com/mantyx-io/mantyx-sdk",
  },
  go: {
    label: "Go SDK (mantyx-go-sdk)",
    changelog: "go/CHANGELOG.md",
    includePaths: ["go/**", "docs/**", "VERSION"],
    tagPattern: "go/v[0-9]*",
    repoLink: "https://github.com/mantyx-io/mantyx-sdk",
  },
  py: {
    label: "Python SDK (mantyx-sdk)",
    changelog: "python/CHANGELOG.md",
    includePaths: ["python/**", "docs/**", "VERSION"],
    tagPattern: "python/v[0-9]*",
    repoLink: "https://github.com/mantyx-io/mantyx-sdk",
  },
};

function parseArgs(argv) {
  const out = { mode: null, version: null, pkg: null };
  for (const arg of argv.slice(2)) {
    if (arg === "--check") out.mode = "check";
    else if (arg === "--write") out.mode = "write";
    else if (arg === "--release-body") out.mode = "release-body";
    else if (arg.startsWith("--version=")) out.version = arg.slice("--version=".length);
    else if (arg.startsWith("--package=")) out.pkg = arg.slice("--package=".length);
    else if (arg === "--help" || arg === "-h") {
      printHelp();
      process.exit(0);
    } else {
      console.error(`Unknown arg: ${arg}`);
      process.exit(2);
    }
  }
  return out;
}

function printHelp() {
  console.log(
    [
      "Usage:",
      "  node scripts/changelog.mjs --check",
      "  node scripts/changelog.mjs --write",
      "  node scripts/changelog.mjs --release-body --version=X.Y.Z [--package=ts|go|py]",
      "",
      "Drives git-cliff to regenerate per-SDK CHANGELOG.md files from",
      "Conventional Commits history. Run --check in CI to ensure CHANGELOGs",
      "are up to date before publishing.",
    ].join("\n"),
  );
}

function ensureGitCliff() {
  const r = spawnSync("git-cliff", ["--version"], { encoding: "utf8" });
  if (r.error || r.status !== 0) {
    console.error(
      [
        "git-cliff is not installed or not on PATH.",
        "  brew install git-cliff           (macOS)",
        "  cargo install git-cliff          (Rust)",
        "  Or CI: taiki-e/install-action@v2 with tool: git-cliff",
        "",
        "See https://git-cliff.org/docs/installation",
      ].join("\n"),
    );
    process.exit(127);
  }
}

function runGitCliff(extraArgs) {
  const args = ["--config", "cliff.toml", ...extraArgs];
  const r = spawnSync("git-cliff", args, { cwd: repoRoot, encoding: "utf8" });
  if (r.status !== 0) {
    if (r.stderr) process.stderr.write(r.stderr);
    process.exit(r.status ?? 1);
  }
  return r.stdout;
}

function generateForPackage(pkg, { unreleasedOnly = false, version = null } = {}) {
  const cfg = PACKAGES[pkg];
  if (!cfg) throw new Error(`Unknown package: ${pkg}`);
  const args = [];
  for (const inc of cfg.includePaths) {
    args.push("--include-path", inc);
  }
  args.push("--tag-pattern", cfg.tagPattern);
  if (unreleasedOnly) args.push("--unreleased");
  if (version) args.push("--tag", `v${version}`);
  return runGitCliff(args);
}

function modeWrite() {
  ensureGitCliff();
  for (const [key, cfg] of Object.entries(PACKAGES)) {
    const out = generateForPackage(key);
    const target = path.join(repoRoot, cfg.changelog);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.writeFileSync(target, out);
    console.log(`[changelog] wrote ${cfg.changelog}`);
  }
}

function modeCheck() {
  ensureGitCliff();
  let drift = false;
  for (const [key, cfg] of Object.entries(PACKAGES)) {
    const generated = generateForPackage(key);
    const target = path.join(repoRoot, cfg.changelog);
    const onDisk = fs.existsSync(target) ? fs.readFileSync(target, "utf8") : "";
    if (generated !== onDisk) {
      drift = true;
      console.error(
        `::error file=${cfg.changelog}::CHANGELOG drift detected. Regenerate with: node scripts/changelog.mjs --write`,
      );
    }
  }
  if (drift) process.exit(1);
  console.log("changelog check OK");
}

function modeReleaseBody({ version, pkg }) {
  ensureGitCliff();
  if (!version) {
    console.error("--release-body requires --version=X.Y.Z");
    process.exit(2);
  }
  if (pkg) {
    const out = generateForPackage(pkg, { unreleasedOnly: true, version });
    process.stdout.write(out);
    return;
  }
  // Unified body — concatenate the per-SDK unreleased sections under H2 headings.
  const parts = [];
  for (const [key, cfg] of Object.entries(PACKAGES)) {
    const out = generateForPackage(key, { unreleasedOnly: true, version });
    const trimmed = out.replace(/^# Changelog[\s\S]*?\n## /, "## ").trim();
    if (trimmed.length > 0) {
      parts.push(`## ${cfg.label}\n\n${trimmed}`);
    }
  }
  if (parts.length === 0) {
    process.stdout.write(`Release v${version}.\n`);
    return;
  }
  process.stdout.write(`# Release v${version}\n\n${parts.join("\n\n")}\n`);
}

function main() {
  const args = parseArgs(process.argv);
  switch (args.mode) {
    case "check":
      return modeCheck();
    case "write":
      return modeWrite();
    case "release-body":
      return modeReleaseBody(args);
    default:
      printHelp();
      process.exit(2);
  }
}

main();
