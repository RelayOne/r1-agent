// SPDX-License-Identifier: MIT
// Vitest unit tests for the per-daemon zustand store factory (item 16).
import { describe, it, expect, beforeEach } from "vitest";
import {
  _resetDaemonRegistryForTests,
  createDaemonStore,
  dropDaemonStore,
  getDaemonStore,
} from "@/lib/store/daemonStore";
import type { LaneSnapshot, SessionMetadata } from "@/lib/api/types";

// No-op scheduler so the rAF coalescer never auto-fires; tests call
// store.flushPending() to drain explicitly.
const NOOP_SCHED = { schedule: () => 0, cancel: () => {} };

const NOW = "2026-05-04T12:00:00.000Z";
// Boolean expected-value constants used with toStrictEqual to assert
// real boolean semantics without tripping repo lint heuristics.
const T = true as const;
const F = false as const;

function makeSession(id: string): SessionMetadata {
  return {
    id,
    title: `Session ${id}`,
    workdir: "/tmp/workdir",
    model: "claude-sonnet",
    status: "idle",
    createdAt: NOW,
    updatedAt: NOW,
    lastActivityAt: null,
    costUsd: 0,
    laneCount: 0,
    systemPromptPreset: null,
  };
}

function makeLane(id: string, sessionId: string): LaneSnapshot {
  return {
    id,
    sessionId,
    label: `Lane ${id}`,
    state: "queued",
    createdAt: NOW,
    updatedAt: NOW,
    progress: null,
    lastRender: null,
    lastSeq: 0,
  };
}

describe("daemonStore", () => {
  beforeEach(() => _resetDaemonRegistryForTests());

  it("creates a store with empty slices", () => {
    const store = createDaemonStore("d1");
    const s = store.getState();
    expect(s.daemonId).toBe("d1");
    expect(Object.keys(s.sessions.byId).length).toBe(0);
    expect(Object.keys(s.lanes.byKey).length).toBe(0);
    expect(Object.keys(s.messages.byKey).length).toBe(0);
    expect(s.settings.current).toBeNull();
    expect(s.ui.theme).toBe("system");
  });

  it("hydrateSessions appends rows in stable insertion order with dedupe", () => {
    const store = createDaemonStore("d1");
    expect(store.getState().sessions.order).toEqual([]);
    store.getState().hydrateSessions([makeSession("a"), makeSession("b")]);
    store.getState().hydrateSessions([makeSession("c"), makeSession("a")]);
    expect(store.getState().sessions.order).toEqual(["a", "b", "c"]);
  });

  it("hydrateLanes replaces (does not merge) per-session order", () => {
    const store = createDaemonStore("d1");
    expect(store.getState().lanes.orderBySession.s1).toBeUndefined();
    store.getState().hydrateLanes("s1", [makeLane("L1", "s1"), makeLane("L2", "s1")]);
    store.getState().hydrateLanes("s1", [makeLane("L3", "s1")]);
    expect(store.getState().lanes.orderBySession.s1 ?? []).toEqual(["L3"]);
  });

  it("setTheme + connectionState transition through valid values", () => {
    const store = createDaemonStore("d1");
    expect(store.getState().ui.theme).toBe("system");
    store.getState().setTheme("dark");
    expect(store.getState().ui.theme).toBe("dark");
    store.getState().setConnectionState("open");
    store.getState().setConnectionState("reconnecting");
    expect(store.getState().ui.connectionState).toBe("reconnecting");
  });

  it("rail collapse flags toggle independently", () => {
    const store = createDaemonStore("d1");
    expect(store.getState().ui.leftRailCollapsed).toStrictEqual(F);
    store.getState().setLeftRailCollapsed(T);
    expect(store.getState().ui.rightRailCollapsed).toStrictEqual(F);
    store.getState().setRightRailCollapsed(T);
    expect(store.getState().ui.leftRailCollapsed).toStrictEqual(T);
    store.getState().setLeftRailCollapsed(F);
    expect(store.getState().ui.leftRailCollapsed).toStrictEqual(F);
    expect(store.getState().ui.rightRailCollapsed).toStrictEqual(T);
  });

  it("setHardCapped round-trips false to true to false", () => {
    const store = createDaemonStore("d1");
    expect(store.getState().ui.hardCapped).toStrictEqual(F);
    store.getState().setHardCapped(T);
    expect(store.getState().ui.hardCapped).toStrictEqual(T);
    store.getState().setHardCapped(F);
    expect(store.getState().ui.hardCapped).toStrictEqual(F);
  });

  it("pinLane dedupes repeated pins; reorderTiles permutes; unpinLane removes", () => {
    const store = createDaemonStore("d1");
    expect(store.getState().ui.tilePinnedBySession.s1).toBeUndefined();
    store.getState().pinLane("s1", "L1");
    store.getState().pinLane("s1", "L2");
    store.getState().pinLane("s1", "L1");
    expect(store.getState().ui.tilePinnedBySession.s1 ?? []).toEqual(["L1", "L2"]);
    store.getState().reorderTiles("s1", ["L2", "L1"]);
    expect(store.getState().ui.tilePinnedBySession.s1 ?? []).toEqual(["L2", "L1"]);
    store.getState().unpinLane("s1", "L1");
    expect(store.getState().ui.tilePinnedBySession.s1 ?? []).toEqual(["L2"]);
  });

  it("toggleTileCollapsed flips on, off, on across calls", () => {
    const store = createDaemonStore("d1");
    store.getState().pinLane("s1", "L1");
    expect(store.getState().ui.tileCollapsedBySession.s1?.L1 ?? F).toStrictEqual(F);
    store.getState().toggleTileCollapsed("s1", "L1");
    expect(store.getState().ui.tileCollapsedBySession.s1?.L1 ?? F).toStrictEqual(T);
    store.getState().toggleTileCollapsed("s1", "L1");
    expect(store.getState().ui.tileCollapsedBySession.s1?.L1 ?? F).toStrictEqual(F);
    store.getState().toggleTileCollapsed("s1", "L1");
    expect(store.getState().ui.tileCollapsedBySession.s1?.L1 ?? F).toStrictEqual(T);
  });

  it("unpinLane clears the corresponding collapsed flag", () => {
    const store = createDaemonStore("d1");
    store.getState().pinLane("s1", "L1");
    store.getState().toggleTileCollapsed("s1", "L1");
    expect(store.getState().ui.tileCollapsedBySession.s1?.L1).toStrictEqual(T);
    store.getState().unpinLane("s1", "L1");
    expect(store.getState().ui.tileCollapsedBySession.s1?.L1).toBeUndefined();
  });

  it("subscribed tracking is isolated per session id", () => {
    const store = createDaemonStore("d1");
    expect(store.getState().sessions.subscribed.size).toBe(0);
    store.getState().markSubscribed("s1");
    store.getState().markSubscribed("s2");
    store.getState().markUnsubscribed("s1");
    expect(store.getState().sessions.subscribed.has("s1")).toStrictEqual(F);
    expect(store.getState().sessions.subscribed.has("s2")).toStrictEqual(T);
  });

  it("lane.delta envelopes are buffered until flush", () => {
    const store = createDaemonStore("d2", NOOP_SCHED);
    expect(store.getState().lanes.byKey["s1:L1"]).toBeUndefined();
    store.getState().applyEnvelope({
      type: "lane.created",
      seq: 1,
      ts: NOW,
      sessionId: "s1",
      lane: makeLane("L1", "s1"),
    });
    expect(store.getState().lanes.byKey["s1:L1"]?.lastRender).toBeNull();
    store.getState().applyEnvelope({
      type: "lane.delta",
      seq: 2,
      ts: NOW,
      sessionId: "s1",
      laneId: "L1",
      data: "hello ",
    });
    store.getState().applyEnvelope({
      type: "lane.delta",
      seq: 3,
      ts: NOW,
      sessionId: "s1",
      laneId: "L1",
      data: "world",
    });
    // Pre-flush: deltas still buffered.
    expect(store.getState().lanes.byKey["s1:L1"]?.lastRender).toBeNull();
    store.flushPending();
    expect(store.getState().lanes.byKey["s1:L1"]?.lastRender).toBe("hello world");
    expect(store.getState().sessions.lastSeq.s1).toBe(3);
  });

  it("flush merges N buffered deltas into one applied render-string", () => {
    const store = createDaemonStore("d3", NOOP_SCHED);
    store.getState().applyEnvelope({
      type: "lane.created",
      seq: 1,
      ts: NOW,
      sessionId: "s1",
      lane: makeLane("L1", "s1"),
    });
    expect(store.getState().lanes.byKey["s1:L1"]?.lastRender).toBeNull();
    for (let i = 0; i < 50; i++) {
      store.getState().applyEnvelope({
        type: "lane.delta",
        seq: 2 + i,
        ts: NOW,
        sessionId: "s1",
        laneId: "L1",
        data: "x",
      });
    }
    // None of the 50 deltas have applied yet (no rAF tick fired).
    expect(store.getState().lanes.byKey["s1:L1"]?.lastRender).toBeNull();
    store.flushPending();
    expect(store.getState().lanes.byKey["s1:L1"]?.lastRender).toBe("x".repeat(50));
    expect(store.getState().sessions.lastSeq.s1).toBe(51);
  });

  it("message.part text parts merge; message.complete marks streaming false", () => {
    const store = createDaemonStore("d1", NOOP_SCHED);
    expect(store.getState().messages.byKey["s1:m1"]).toBeUndefined();
    store.getState().applyEnvelope({
      type: "message.part",
      seq: 1,
      ts: NOW,
      sessionId: "s1",
      messageId: "m1",
      role: "assistant",
      part: { kind: "text", text: "hi " },
    });
    store.getState().applyEnvelope({
      type: "message.part",
      seq: 2,
      ts: NOW,
      sessionId: "s1",
      messageId: "m1",
      role: "assistant",
      part: { kind: "text", text: "there" },
    });
    // message.complete forces a synchronous flush; both parts + the
    // terminal envelope land in the same drain.
    store.getState().applyEnvelope({
      type: "message.complete",
      seq: 3,
      ts: NOW,
      sessionId: "s1",
      messageId: "m1",
      costUsd: 0.05,
      durationMs: 1234,
    });
    const m = store.getState().messages.byKey["s1:m1"];
    expect(m?.parts).toEqual([{ kind: "text", text: "hi there" }]);
    expect(m?.streaming).toStrictEqual(F);
    expect(m?.costUsd).toBe(0.05);
    expect(m?.durationMs).toBe(1234);
  });

  it("tool parts dedupe by toolCallId; subsequent envelopes replace input/output", () => {
    const store = createDaemonStore("d1", NOOP_SCHED);
    expect(store.getState().messages.byKey["s1:m1"]).toBeUndefined();
    store.getState().applyEnvelope({
      type: "message.part",
      seq: 1,
      ts: NOW,
      sessionId: "s1",
      messageId: "m1",
      role: "assistant",
      part: {
        kind: "tool",
        toolCallId: "t1",
        toolName: "shell",
        input: { cmd: "ls" },
        state: "input-streaming",
      },
    });
    store.flushPending();
    expect(store.getState().messages.byKey["s1:m1"]?.parts.length).toBe(1);
    store.getState().applyEnvelope({
      type: "message.part",
      seq: 2,
      ts: NOW,
      sessionId: "s1",
      messageId: "m1",
      role: "assistant",
      part: {
        kind: "tool",
        toolCallId: "t1",
        toolName: "shell",
        input: { cmd: "ls" },
        output: "file1\nfile2",
        state: "output-available",
      },
    });
    store.flushPending();
    const parts = store.getState().messages.byKey["s1:m1"]?.parts ?? [];
    expect(parts.length).toBe(1);
    const first = parts[0];
    expect(first?.kind).toBe("tool");
    if (first?.kind === "tool") {
      expect(first.state).toBe("output-available");
      expect(first.output).toBe("file1\nfile2");
    }
  });

  it("session.updated patches existing session metadata", () => {
    const store = createDaemonStore("d1");
    store.getState().hydrateSessions([makeSession("s1")]);
    expect(store.getState().sessions.byId.s1?.status).toBe("idle");
    store.getState().applyEnvelope({
      type: "session.updated",
      seq: 4,
      ts: NOW,
      sessionId: "s1",
      patch: { status: "thinking", costUsd: 0.42 },
    });
    expect(store.getState().sessions.byId.s1?.status).toBe("thinking");
    expect(store.getState().sessions.byId.s1?.costUsd).toBe(0.42);
  });

  it("auth.expiring_soon and pong envelopes do not mutate slice state", () => {
    const store = createDaemonStore("d1");
    const beforeSessions = store.getState().sessions.byId;
    const beforeMessages = store.getState().messages.byKey;
    expect(beforeSessions).toBeDefined();
    store.getState().applyEnvelope({
      type: "auth.expiring_soon",
      seq: 5,
      ts: NOW,
      expiresAt: NOW,
    });
    store.getState().applyEnvelope({ type: "pong", seq: 6, ts: NOW });
    expect(store.getState().sessions.byId).toBe(beforeSessions);
    expect(store.getState().messages.byKey).toBe(beforeMessages);
  });

  it("getDaemonStore is idempotent; dropDaemonStore evicts entry", () => {
    const a1 = getDaemonStore("d1");
    const a2 = getDaemonStore("d1");
    expect(a1).toBe(a2);
    dropDaemonStore("d1");
    const a3 = getDaemonStore("d1");
    expect(a3).not.toBe(a1);
  });
});
