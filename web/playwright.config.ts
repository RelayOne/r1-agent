// SPDX-License-Identifier: MIT
// Playwright e2e config for r1-web (web-chat-ui spec).
// Browsers: chromium + firefox + webkit (per spec item 6).
// baseURL points at the local r1d daemon default port.
//
// Test files: src/test/e2e/*.spec.ts plus *.agent.feature.md
// (markdown-driven flows consumed by Playwright MCP per spec 8).
import { defineConfig, devices } from "@playwright/test";

const PORT = Number(process.env.R1D_PORT ?? 7777);
const HOST = process.env.R1D_HOST ?? "127.0.0.1";

export default defineConfig({
  testDir: "./src/test/e2e",
  testMatch: ["**/*.spec.ts"],
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 2 : undefined,
  reporter: process.env.CI ? [["github"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: `http://${HOST}:${PORT}`,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
    {
      name: "firefox",
      use: { ...devices["Desktop Firefox"] },
    },
    {
      name: "webkit",
      use: { ...devices["Desktop Safari"] },
    },
  ],
});
