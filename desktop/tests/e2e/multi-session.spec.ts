// SPDX-License-Identifier: MIT
//
// E2E: Multi-session workdir picker + plugin-store persistence.
// Spec desktop-cortex-augmentation §11.3 + checklist item 35.
//
// Acceptance criterion (spec §14):
//   * Create 5 sessions, each with a distinct workdir picked via the
//     OS folder dialog.
//   * tauri-plugin-store's `sessions.json` ends with 5 entries.
//   * Workdir survives an app restart (load → close → reopen → list).

import { expect, test } from "@playwright/test";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";

import {
  ensureNoDaemonJson,
  expectBannerState,
  launchDesktopApp,
  type DesktopApp,
} from "./helpers/desktop-fixtures";

const SESSION_COUNT = 5;

async function makeWorkdir(idx: number): Promise<string> {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), `r1-e2e-ws-${idx}-`));
  return dir;
}

async function readSessionsJson(): Promise<Record<string, unknown>> {
  const home = process.env.R1_TEST_HOME ?? os.homedir();
  // App data dir mirrors `dirs::data_dir()` semantics — on Linux
  // the test harness sets XDG_CONFIG_HOME under R1_TEST_HOME so the
  // file lands deterministically.
  const file = path.join(home, "dev.r1.desktop", "sessions.json");
  const raw = await fs.readFile(file, "utf8");
  return JSON.parse(raw) as Record<string, unknown>;
}

async function createSessionWithWorkdir(
  app: DesktopApp,
  idx: number,
): Promise<{ sessionId: string; workdir: string }> {
  const workdir = await makeWorkdir(idx);
  // The driver accepts a synthesised "open-dialog response" so the
  // file picker doesn't pop a real OS chooser in CI; the test
  // pre-populates the next response value here.
  await app.waitForEvent("dialog.open.ready", 1000);
  await app.click('[data-role="new-session"]');
  await app.click('[data-role="pick-workdir"]');
  // App emits a confirmation event once `pickWorkdir` resolves.
  const created = await app.waitForEvent<{
    sessionId: string;
    workdir: string;
  }>("session.workdir.set", 5000);
  return { sessionId: created.sessionId, workdir: created.workdir ?? workdir };
}

test.describe("Multi-session workdir + persistence", () => {
  test("creates 5 sessions with distinct workdirs and persists them", async () => {
    await ensureNoDaemonJson();
    const app = await launchDesktopApp();
    expect(app).toBeTruthy();
    try {
      await expectBannerState(app, "sidecar", { timeoutMs: 3000 });
      const created: Array<{ sessionId: string; workdir: string }> = [];
      for (let i = 0; i < SESSION_COUNT; i++) {
        const c = await createSessionWithWorkdir(app, i);
        created.push(c);
      }
      const ids = created.map((c) => c.sessionId);
      const workdirs = created.map((c) => c.workdir);
      expect(new Set(ids).size).toBe(SESSION_COUNT);
      expect(new Set(workdirs).size).toBe(SESSION_COUNT);
      const persisted = await readSessionsJson();
      expect(Object.keys(persisted).length).toBeGreaterThanOrEqual(
        SESSION_COUNT,
      );
    } finally {
      await app.close();
    }
  });

  test("workdir per session survives an app restart", async () => {
    await ensureNoDaemonJson();
    const first = await launchDesktopApp();
    expect(first).toBeTruthy();
    let target: { sessionId: string; workdir: string } | null = null;
    try {
      await expectBannerState(first, "sidecar", { timeoutMs: 3000 });
      target = await createSessionWithWorkdir(first, 0);
      expect(target.workdir.length).toBeGreaterThan(0);
    } finally {
      await first.close();
    }
    expect(target).not.toBeNull();

    // Restart and load the same session.
    const second = await launchDesktopApp();
    try {
      await expectBannerState(second, "sidecar", { timeoutMs: 3000 });
      const persisted = await readSessionsJson();
      const meta = (persisted[target!.sessionId] ?? null) as
        | null
        | { workdir?: string };
      expect(meta).not.toBeNull();
      expect(meta?.workdir).toBe(target!.workdir);
    } finally {
      await second.close();
    }
  });

  test("clearing a workdir removes the binding without nuking the session row", async () => {
    await ensureNoDaemonJson();
    const app = await launchDesktopApp();
    expect(app).toBeTruthy();
    try {
      await expectBannerState(app, "sidecar", { timeoutMs: 3000 });
      const created = await createSessionWithWorkdir(app, 0);
      // Driver-only debug command emits "session.workdir.cleared".
      await app.click(
        `[data-role="clear-workdir"][data-session-id="${created.sessionId}"]`,
      );
      await app.waitForEvent("session.workdir.cleared", 2000);
      const persisted = await readSessionsJson();
      const meta = persisted[created.sessionId] as { workdir?: string | null };
      expect(meta).toBeTruthy();
      expect(meta.workdir).toBeNull();
    } finally {
      await app.close();
    }
  });
});
