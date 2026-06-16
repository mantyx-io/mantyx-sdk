#!/usr/bin/env node
/**
 * Bump the repo root VERSION file (semver).
 *
 * Usage:
 *   node scripts/bump-version.mjs patch|minor|major
 *   node scripts/bump-version.mjs 1.2.3          # explicit version
 *   node scripts/bump-version.mjs --check 1.2.3  # validate only, no write
 *
 * Prints the new version to stdout (one line) for workflow GITHUB_OUTPUT.
 */
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const versionFile = path.join(__dirname, "..", "VERSION");

const SEMVER_RE = /^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$/;

function readVersion() {
  return fs.readFileSync(versionFile, "utf8").trim();
}

function validateSemver(v) {
  if (!SEMVER_RE.test(v)) {
    console.error("VERSION must be a semver string, got:", JSON.stringify(v));
    process.exit(1);
  }
}

function parseCore(v) {
  const [core, prerelease] = v.split("-", 2);
  const [major, minor, patch] = core.split(".").map(Number);
  return { major, minor, patch, prerelease };
}

function bump(current, kind) {
  const { major, minor, patch } = parseCore(current);
  switch (kind) {
    case "patch":
      return `${major}.${minor}.${patch + 1}`;
    case "minor":
      return `${major}.${minor + 1}.0`;
    case "major":
      return `${major + 1}.0.0`;
    default:
      console.error(`Unknown bump kind: ${kind}. Use patch, minor, or major.`);
      process.exit(2);
  }
}

function resolveTarget(argv) {
  const checkOnly = argv.includes("--check");
  const args = argv.filter((a) => a !== "--check");
  if (args.length !== 1) {
    console.error("Usage: node scripts/bump-version.mjs [--check] patch|minor|major|X.Y.Z");
    process.exit(2);
  }
  const arg = args[0];
  const current = readVersion();
  validateSemver(current);

  let target;
  if (arg === "patch" || arg === "minor" || arg === "major") {
    target = bump(current, arg);
  } else {
    target = arg.trim();
    validateSemver(target);
  }

  if (target === current && (arg === "patch" || arg === "minor" || arg === "major")) {
    console.error(`Bump ${arg} from ${current} produced the same version.`);
    process.exit(1);
  }

  return { current, target, checkOnly };
}

function main() {
  const { target, checkOnly } = resolveTarget(process.argv.slice(2));
  if (checkOnly) {
    console.log(target);
    return;
  }
  fs.writeFileSync(versionFile, `${target}\n`);
  console.log(target);
}

main();
