// SPDX-License-Identifier: MIT
//
// E2E: Lane pop-out lifecycle.
// Spec desktop-cortex-augmentation §11.3 + checklist item 37.
//
// Acceptance criterion (spec §14):
//   * Pop a lane via the menu (or the focus-view button) → assert a
//     new WebviewWindow opens with label "lane:<session>:<lane>".
//   * Close the primary window → assert pop-outs remain (independent
//     window lifecycle).
//   * Re-popping an already-open lane focuses the existing window
//     (idempotency from popout.rs).

import { expect, test } from "@playwright/test";

import {
  ensureNoDaemonJson,
  expectBannerState,
  launchDesktopApp,
  type DesktopApp,
} from "./helpers/desktop-fixtures";

interface OpenWindow {
  label: string;
  url: string;
}

async function listOpenWindows(app: DesktopApp): Promise<OpenWindow[]> {
  // Driver-only debug verb that enumerates the live WebviewWindow set.
  return await app.waitForEvent<OpenWindow[]>("test.windows.list", 2000);
}

async function popOutLane(
  app: DesktopApp,
  sessionId: string,
  laneId: string,
): Promise<string> {
  await app.click(
    `[data-role="popout-lane"][data-session-id="${sessionId}"][data-lane-id="${laneId}"]`,
  );
  // The host emits "popout.opened" with the new window's label.
  const evt = await app.waitForEvent<{ window_label: string }>(
    "popout.opened",
    3000,
  );
  return evt.window_label;
}

test.describe("Lane pop-out lifecycle", () => {
  test("pop-out via menu opens lane:<session>:<lane> WebviewWindow", async () => {
    await ensureNoDaemonJson();
    const app = await launchDesktopApp({ withFakeExternalDaemon: true });
    expect(app).toBeTruthy();
    try {
      await expectBannerState(app, "external", { timeoutMs: 2000 });
      const sessionId = "S-pop";
      const laneId = "L-1";
      const label = await popOutLane(app, sessionId, laneId);
      expect(label).toBe(`lane:${sessionId}:${laneId}`);
      const windows = await listOpenWindows(app);
      const popout = windows.find((w) => w.label === label);
      expect(popout).toBeTruthy();
      expect(popout?.url).toContain("popout=lane");
    } finally {
      await app.close();
    }
  });

  test("pop-outs survive primary-window close", async () => {
    await ensureNoDaemonJson();
    const app = await launchDesktopApp({ withFakeExternalDaemon: true });
    expect(app).toBeTruthy();
    try {
      await expectBannerState(app, "external", { timeoutMs: 2000 });
      const label = await popOutLane(app, "S-1", "L-2");
      // Close the primary window via debug verb (preserves the
      // app process so pop-outs can outlive it).
      await app.click('[data-role="close-primary-window"]');
      await app.waitForEvent("primary-window.closed", 2000);
      const windows = await listOpenWindows(app);
      const popout = windows.find((w) => w.label === label);
      expect(popout).toBeTruthy();
      expect(windows.length).toBeGreaterThanOrEqual(1);
    } finally {
      await app.close();
    }
  });

  test("re-popping an already-open lane focuses the existing window", async () => {
    await ensureNoDaemonJson();
    const app = await launchDesktopApp({ withFakeExternalDaemon: true });
    expect(app).toBeTruthy();
    try {
      await expectBannerState(app, "external", { timeoutMs: 2000 });
      const sessionId = "S-1";
      const laneId = "L-3";
      const labelA = await popOutLane(app, sessionId, laneId);
      const labelB = await popOutLane(app, sessionId, laneId);
      expect(labelA).toBe(labelB);
      const windows = await listOpenWindows(app);
      const matching = windows.filter((w) => w.label === labelA);
      // Idempotency: only ONE window with this label, even after two
      // popout calls.
      expect(matching.length).toBe(1);
    } finally {
      await app.close();
    }
  });

  test("closing a pop-out deregisters its registry entry", async () => {
    await ensureNoDaemonJson();
    const app = await launchDesktopApp({ withFakeExternalDaemon: true });
    expect(app).toBeTruthy();
    try {
      await expectBannerState(app, "external", { timeoutMs: 2000 });
      const sessionId = "S-2";
      const laneId = "L-1";
      const label = await popOutLane(app, sessionId, laneId);
      const before = await listOpenWindows(app);
      expect(before.find((w) => w.label === label)).toBeTruthy();
      // Close that pop-out (tauri-driver routes to its window's close).
      await app.click(
        `[data-role="close-popout"][data-window-label="${label}"]`,
      );
      await app.waitForEvent("popout.closed", 2000);
      const after = await listOpenWindows(app);
      expect(after.find((w) => w.label === label)).toBeUndefined();
    } finally {
      await app.close();
    }
  });
});
