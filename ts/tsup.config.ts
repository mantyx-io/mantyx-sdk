import { defineConfig } from "tsup";

export default defineConfig({
  entry: ["src/index.ts", "src/a2a-server.ts"],
  format: ["esm", "cjs"],
  dts: true,
  clean: true,
  sourcemap: true,
  target: "node18",
  outDir: "dist",
  // Don't bundle peer-only deps; users opt-in by installing them.
  external: ["@a2a-js/sdk", "express", "@a2a-js/sdk/server", "@a2a-js/sdk/server/express"],
});
