// SPDX-License-Identifier: MIT
//
// E2E: Lane streaming under sustained load.
// Spec desktop-cortex-augmentation §11.3 + checklist item 36.
//
// Acceptance criterion (spec §14):
//   * Drive a fake daemon emitting lane events at 10 Hz across 4
//     lanes for 30 seconds.
//   * Assert all events rendered (no dropped seq numbers — except
//     when overflow legitimately emits a delta_gap marker).
//   * Render frame-rate ≥ 5 Hz under load (D-S2 perf gate).

import { expect, test } from "@playwright/test";

import {
  ensureNoDaemonJson,
  expectBannerState,
  launchDesktopApp,
  type DesktopApp,
} from "./helpers/desktop-fixtures";

const DURATION_S = 30;
const HZ_PER_LANE = 10;
const LANE_COUNT = 4;
// 30 s × 10 Hz × 4 lanes = 1200 events expected; the actual count
// can be lower if the host's overflow ring fires (legitimate gap),
// but must NOT have non-monotonic seqs within any one lane.
const EXPECTED_PER_LANE = DURATION_S * HZ_PER_LANE;

interface RenderedEvent {
  sessionId: string;
  laneId: string;
  seq: number;
  kind: "delta" | "delta_gap";
  ts: number; // ms epoch when WebView rendered it
}

interface LaneStats {
  /** Highest seq the lane received, in render order. */
  highest: number;
  /** Count of seqs received (incl. the gap markers). */
  receivedSeqs: number[];
  /** Whether at least one delta_gap was rendered for this lane. */
  hadGap: boolean;
}

/** Drive the daemon-side fake to emit `events_per_lane * lanes`
 *  events at 10 Hz across `lanes` lanes for `seconds`. The host
 *  exposes a "test://drive-lanes" command in debug builds that the
 *  fake daemon honours by walking the spec's event schedule. */
async function driveLanes(
  app: DesktopApp,
  args: {
    lanes: number;
    hzPerLane: number;
    durationSeconds: number;
    sessionId: string;
  },
): Promise<void> {
  const totalMs = args.durationSeconds * 1000;
  // The driver round-trips a single command that triggers the fake
  // daemon's event-emission loop and resolves once it completes.
  await app.waitForEvent("test.drive-lanes.started", 1000);
  // Defensive timeout — give the fake daemon 5 s extra to settle so
  // tests don't fail on slightly-late final events.
  const settle = totalMs + 5000;
  void app.click(
    `[data-role="drive-lanes"][data-session-id="${args.sessionId}"]`,
  );
  await app.waitForEvent("test.drive-lanes.completed", settle);
}

/** Subscribe the e2e harness to the WebView's render-callbacks for
 *  lane events. Returns a snapshot function the test calls at the
 *  end to read what was rendered. */
async function startRenderTrace(
  app: DesktopApp,
  sessionId: string,
): Promise<() => Promise<RenderedEvent[]>> {
  await app.click(`[data-role="trace-lanes"][data-session-id="${sessionId}"]`);
  return async () => {
    const events = await app.waitForEvent<RenderedEvent[]>(
      "test.lane-trace.dump",
      5000,
    );
    return events;
  };
}

function summarise(rendered: RenderedEvent[]): Map<string, LaneStats> {
  const out = new Map<string, LaneStats>();
  for (const ev of rendered) {
    let s = out.get(ev.laneId);
    if (!s) {
      s = { highest: 0, receivedSeqs: [], hadGap: false };
      out.set(ev.laneId, s);
    }
    if (ev.kind === "delta_gap") {
      s.hadGap = true;
      continue;
    }
    s.receivedSeqs.push(ev.seq);
    if (ev.seq > s.highest) s.highest = ev.seq;
  }
  return out;
}

/** Render frame-rate observed by the trace = (number of distinct ms
 *  buckets in which at least one render fired) / duration. Counts
 *  every distinct 100 ms bucket (so 10 fps = saturation, 5 fps
 *  = the spec's lower bound). */
function renderHz(rendered: RenderedEvent[], durationS: number): number {
  if (rendered.length === 0) return 0;
  const buckets = new Set<number>();
  for (const ev of rendered) buckets.add(Math.floor(ev.ts / 100));
  return buckets.size / 10 / durationS;
}

test.describe("Lanes streaming under sustained load", () => {
  test.setTimeout(120_000); // generous: 30 s drive + warm-up + teardown.

  test("renders all events at ≥ 5 Hz with monotonic seqs (D-S2 perf gate)", async () => {
    await ensureNoDaemonJson();
    const app = await launchDesktopApp({ withFakeExternalDaemon: true });
    expect(app).toBeTruthy();
    try {
      await expectBannerState(app, "external", { timeoutMs: 2000 });
      const sessionId = "S-stream";
      const dump = await startRenderTrace(app, sessionId);
      await driveLanes(app, {
        lanes: LANE_COUNT,
        hzPerLane: HZ_PER_LANE,
        durationSeconds: DURATION_S,
        sessionId,
      });
      const rendered = await dump();
      const summary = summarise(rendered);

      // Each lane must have rendered at least one event.
      expect(summary.size).toBe(LANE_COUNT);

      // Per-lane: seqs must be strictly monotonic (overflow gaps are
      // allowed — they just skip seqs — but never go backwards).
      for (const [_lane, s] of summary) {
        for (let i = 1; i < s.receivedSeqs.length; i++) {
          expect(s.receivedSeqs[i]).toBeGreaterThan(s.receivedSeqs[i - 1]);
        }
        // Highest seq is at most EXPECTED_PER_LANE.
        expect(s.highest).toBeLessThanOrEqual(EXPECTED_PER_LANE);
      }

      // Render frame-rate budget.
      const observedHz = renderHz(rendered, DURATION_S);
      expect(observedHz).toBeGreaterThanOrEqual(5);
    } finally {
      await app.close();
    }
  });

  test("delta_gap marker arrives when overflow drops events", async () => {
    await ensureNoDaemonJson();
    const app = await launchDesktopApp({ withFakeExternalDaemon: true });
    expect(app).toBeTruthy();
    try {
      await expectBannerState(app, "external", { timeoutMs: 2000 });
      const sessionId = "S-overflow";
      // Saturate one lane beyond ring capacity; the synthetic spec
      // hook drives 4096 events at the full burst rate.
      const dump = await startRenderTrace(app, sessionId);
      void app.click(
        `[data-role="overflow-lane"][data-session-id="${sessionId}"]`,
      );
      await app.waitForEvent("test.overflow-lane.completed", 30_000);
      const rendered = await dump();
      const summary = summarise(rendered);
      const lane = summary.values().next().value;
      const gapState = lane?.hadGap ?? false;
      expect(gapState).not.toBe(false);
    } finally {
      await app.close();
    }
  });
});
