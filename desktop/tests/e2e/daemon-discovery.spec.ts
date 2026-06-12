// SPDX-License-Identifier: MIT
//
// E2E: Daemon discovery + sidecar fallback.
// Spec desktop-cortex-augmentation §11.3 + checklist item 34.
//
// Acceptance criterion (spec §14):
//   * Cold start with no `~/.r1/daemon.json` spawns the bundled
//     sidecar within 3 s and shows the blue "Bundled daemon" banner.
//   * Killing the sidecar PID flips the banner red; clicking
//     Reconnect re-spawns and the banner returns to blue.
//
// The driver here is tauri-driver + Playwright (per
// docs/decisions/index.md D-A3). We use the Playwright MCP server
// scaffolding so the agentic test harness (spec 8) can drive these
// without a host CI build. Test bodies remain green-pathed; CI
// invokes them under `npm run e2e` which routes through
// tauri-driver.
//
// NOTE: this file is a Playwright spec — it depends on
// `@playwright/test` which the desktop/ package will pin once the
// e2e job is enabled. Running locally requires `npm install
// @playwright/test` + `npx playwright install`.

import { expect, test } from "@playwright/test";

import {
  ensureNoDaemonJson,
  expectBannerState,
  killSidecarPid,
  launchDesktopApp,
  readDaemonJson,
} from "./helpers/desktop-fixtures";

// SKIPPED-WITH-REASON (CI-2 desktop-e2e-truth, 2026-06-12): the desktop
// app exposes NO test-mode driver surface. These specs require a
// `testState` verb backing the "test://daemon-status" hook, host-side
// handling of R1_E2E / R1_FAKE_EXTERNAL_DAEMON, and a clickable
// `[data-role="r1-daemon-pill"]` driver route — none of which exist in
// desktop/src or desktop/src-tauri (grep evidence in
// audit/desktop-e2e-truth-2026-06-12.md). Un-skip only when the app
// ships a real, non-production-gated test surface; never fabricate
// hook responses to force green.
test.skip(
  true,
  "app has no test-mode driver surface (testState / R1_E2E / r1-daemon-pill) — see audit/desktop-e2e-truth-2026-06-12.md (CI-2)",
);

test.describe("Daemon discovery + sidecar fallback", () => {
  test("cold start with no daemon spawns sidecar within 3 s", async () => {
    await ensureNoDaemonJson();
    const app = await launchDesktopApp();
    expect(app).toBeTruthy();
    try {
      const banner = await expectBannerState(app, "sidecar", {
        timeoutMs: 3000,
      });
      expect(banner.kind).toBe("sidecar");
    } finally {
      await app.close();
    }
  });

  test("kill sidecar PID → banner red → Reconnect → blue again", async () => {
    await ensureNoDaemonJson();
    const app = await launchDesktopApp();
    expect(app).toBeTruthy();
    try {
      const initial = await expectBannerState(app, "sidecar", {
        timeoutMs: 3000,
      });
      expect(initial.kind).toBe("sidecar");
      const info = await readDaemonJson(app);
      await killSidecarPid(info.pid);
      const offline = await expectBannerState(app, "offline", {
        timeoutMs: 5000,
      });
      expect(offline.kind).toBe("offline");
      await app.click('[data-role="r1-daemon-pill"]');
      const recovered = await expectBannerState(app, "sidecar", {
        timeoutMs: 3000,
      });
      expect(recovered.kind).toBe("sidecar");
    } finally {
      await app.close();
    }
  });

  test("external daemon takes precedence over sidecar at startup", async () => {
    await ensureNoDaemonJson();
    const fakeDaemon = await launchDesktopApp({
      withFakeExternalDaemon: true,
    });
    expect(fakeDaemon).toBeTruthy();
    try {
      const banner = await expectBannerState(fakeDaemon, "external", {
        timeoutMs: 1000,
      });
      expect(banner.kind).toBe("external");
    } finally {
      await fakeDaemon.close();
    }
  });
});
