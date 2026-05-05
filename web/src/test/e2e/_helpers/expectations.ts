// SPDX-License-Identifier: MIT
// Shared Playwright assertion helpers for items 43 (CSP zero-violation)
// and 44 (axe-core a11y enforcement).
//
// CSP enforcement (item 43):
//   `attachCspWatcher(page)` collects every console.error message that
//   matches /Content Security Policy/ during the page's lifetime.
//   `assertNoCspViolations(handle)` asserts the collected list is
//   empty. Each test in the e2e suite is expected to call
//   attachCspWatcher() before navigation, then assertNoCspViolations()
//   in an afterEach (or at end of test).
//
// Accessibility enforcement (item 44):
//   `assertAxeClean(page)` runs @axe-core/playwright's AxeBuilder
//   against the active page, filters findings to severity in
//   {serious, critical}, and fails the test if any remain. Caller
//   passes the route under test for clearer error messages.
import type { Page } from "@playwright/test";
import { expect } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

export interface CspWatcherHandle {
  /** Mutable list of CSP violation messages collected so far. */
  violations: string[];
}

const CSP_REGEX = /Content Security Policy/i;

export function attachCspWatcher(page: Page): CspWatcherHandle {
  const handle: CspWatcherHandle = { violations: [] };
  page.on("console", (msg) => {
    if (msg.type() === "error" && CSP_REGEX.test(msg.text())) {
      handle.violations.push(msg.text());
    }
  });
  page.on("pageerror", (err) => {
    if (CSP_REGEX.test(err.message)) {
      handle.violations.push(err.message);
    }
  });
  return handle;
}

export function assertNoCspViolations(handle: CspWatcherHandle): void {
  expect(
    handle.violations,
    `expected zero CSP violations, found ${handle.violations.length}:\n${handle.violations.join("\n")}`,
  ).toEqual([]);
}

export async function assertAxeClean(
  page: Page,
  routeLabel: string,
): Promise<void> {
  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();
  const offenders = results.violations.filter(
    (v) => v.impact === "serious" || v.impact === "critical",
  );
  expect(
    offenders,
    `axe found serious/critical a11y violations on ${routeLabel}:\n${offenders
      .map(
        (v) =>
          `  - ${v.id} (${v.impact}) — ${v.help}: ${v.helpUrl}\n    nodes: ${v.nodes.length}`,
      )
      .join("\n")}`,
  ).toEqual([]);
}
