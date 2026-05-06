// SPDX-License-Identifier: MIT
// Playwright e2e config for r1-desktop.
//
// Spec desktop-cortex-augmentation §11.3 + items 34..37. Without an
// explicit config, `npx playwright test` falls back to a default
// glob (`**/*.{spec,test}.{js,ts,...}`) that matches the desktop's
// vitest unit tests under `src/` (e.g. `src/r1d-1.test.ts`). Those
// files import `vitest` and transitively pull in `@vitest/expect`,
// which playwright then loads a second time via its own runner —
// producing the `Cannot redefine property: Symbol($$jest-matchers-object)`
// crash and the `Named export 'LaneSidebar' not found` follow-up
// (the latter from `panels/lane-rail.ts` being walked by playwright's
// resolver but not its TypeScript ESM transform).
//
// Scoping discovery to `tests/e2e/**/*.spec.ts` keeps the two
// runners in their lanes: vitest owns `src/**/*.test.ts`, playwright
// owns `tests/e2e/**/*.spec.ts`.
import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/e2e",
  testMatch: ["**/*.spec.ts"],
  // Fail fast on stray `test.only` in CI.
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 2 : undefined,
  reporter: process.env.CI ? [["github"], ["html", { open: "never" }]] : "list",
});
