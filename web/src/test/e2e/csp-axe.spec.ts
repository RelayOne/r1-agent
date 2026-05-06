// SPDX-License-Identifier: MIT
// CSP zero-violation + axe-core a11y enforcement on every route.
// Spec items 43 + 44 / 55.
//
// Iterates over every shipped route and asserts:
//   - the page loads without any console.error matching
//     /Content Security Policy/ (item 43);
//   - axe-core reports zero serious/critical findings (item 44).
//
// The e2e harness assumes `r1 serve --web=:7777` is running and the
// dist bundle is mounted; CI bootstraps that before the playwright
// step.
import { test, expect } from "@playwright/test";
import {
  attachCspWatcher,
  assertNoCspViolations,
  assertAxeClean,
} from "./_helpers/expectations";

const ROUTES: ReadonlyArray<{ path: string; label: string }> = [
  { path: "/", label: "DaemonsLanding" },
  { path: "/d/local", label: "DaemonHome" },
  { path: "/d/local/sessions/seed-session", label: "SessionView" },
  {
    path: "/d/local/sessions/seed-session/lanes/seed-lane",
    label: "LaneFocus",
  },
  { path: "/settings", label: "Settings" },
  { path: "/no-such-route", label: "NotFound" },
];

async function runRouteCheck(
  page: import("@playwright/test").Page,
  path: string,
  label: string,
): Promise<void> {
  const csp = attachCspWatcher(page);
  const response = await page.goto(path, { waitUntil: "networkidle" });
  expect(response, `navigation to ${path} returned no response`).not.toBeNull();
  assertNoCspViolations(csp);
  await assertAxeClean(page, label);
}

for (const route of ROUTES) {
  test(`route ${route.label} (${route.path}) emits zero CSP errors and clears axe`, async ({ page }) => {
    expect(route.path.length).toBeGreaterThan(0);
    await runRouteCheck(page, route.path, route.label);
  });
}
