import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { SDK_VERSION } from "../src/version.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

describe("SDK_VERSION", () => {
  it("matches the repo root VERSION file and package.json", () => {
    const tsDir = path.join(__dirname, "..");
    const root = path.join(tsDir, "..");
    const v = readFileSync(path.join(root, "VERSION"), "utf8").trim();
    const pkg = JSON.parse(
      readFileSync(path.join(tsDir, "package.json"), "utf8"),
    ) as { version: string };
    expect(SDK_VERSION).toBe(v);
    expect(pkg.version).toBe(v);
  });
});
