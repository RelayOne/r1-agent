// SPDX-License-Identifier: MIT
// Vitest configuration for R1 Desktop panel tests.
import { defineConfig } from "vitest/config";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  test: {
    environment: "jsdom",
    globals: false,
    include: ["src/**/*.test.ts"],
    alias: {
      "@panels": resolve(root, "src/panels"),
      "@types": resolve(root, "src/types"),
    },
  },
});
