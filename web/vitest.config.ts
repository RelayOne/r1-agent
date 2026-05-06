// SPDX-License-Identifier: MIT
// Vitest config for r1-web. jsdom env, MSW setup file at
// src/test/setup.ts (created in later items). Mirrors path alias
// from vite.config.ts.
import { defineConfig } from "vitest/config";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  test: {
    environment: "jsdom",
    globals: false,
    setupFiles: ["./src/test/setup.ts"],
    include: [
      "src/**/*.test.ts",
      "src/**/*.test.tsx",
      // Spec 2 / Spec 5: the v2 graph worker tests live colocated
      // with their source under cmd/r1-server/ui/js/. Reached via a
      // relative path from the web/ workspace root. (Pre-Spec-D the
      // path was cmd/r1-server/ui/web/js/; Spec D lifted ui/web/* up
      // one level and dropped the legacy v1 SPA tree.)
      "../cmd/r1-server/ui/js/*.test.js",
    ],
    exclude: ["src/test/e2e/**", "node_modules/**", "dist/**"],
    coverage: {
      provider: "v8",
      reporter: ["text", "html", "lcov"],
      include: ["src/components/**", "src/lib/**", "src/hooks/**"],
      exclude: ["src/**/*.test.{ts,tsx}", "src/test/**"],
      thresholds: {
        statements: 80,
        branches: 70,
        functions: 80,
        lines: 80,
      },
    },
  },
  resolve: {
    alias: {
      "@": resolve(root, "src"),
      // Spec 2 / TASK-10: graph-worker.js imports d3-force-3d as a
      // bare specifier resolved through the browser import map at
      // runtime. In the vitest harness we alias it to a tiny stub
      // because the test injects its own simulation factory and
      // never executes the real one — and the vendored blob is a
      // UMD bundle that doesn't load cleanly in Node anyway.
      "d3-force-3d": resolve(root, "src/test/d3-force-3d-stub.ts"),
    },
  },
});
