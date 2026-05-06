// SPDX-License-Identifier: MIT
import { describe, it, expect, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { StatusBar } from "@/components/StatusBar";
import {
  _resetDaemonRegistryForTests,
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";
import type { LaneSnapshot, SessionMetadata } from "@/lib/api/types";

const NOOP = { schedule: () => 0, cancel: () => {} };
const NOW = "2026-05-04T12:30:00.000Z";

function mkSession(over: Partial<SessionMetadata> & { id: string }): SessionMetadata {
  const { id, ...rest } = over;
  return {
    id,
    title: id,
    workdir: "/repo",
    model: "claude",
    status: "idle",
    createdAt: NOW,
    updatedAt: NOW,
    lastActivityAt: null,
    costUsd: 0,
    laneCount: 0,
    systemPromptPreset: null,
    ...rest,
  } as SessionMetadata;
}

function mkLane(over: Partial<LaneSnapshot> & { id: string }): LaneSnapshot {
  const { id, ...rest } = over;
  return {
    id,
    sessionId: "s-1",
    label: id,
    state: "running",
    createdAt: NOW,
    updatedAt: NOW,
    progress: null,
    lastRender: null,
    lastSeq: 0,
    ...rest,
  } as LaneSnapshot;
}

describe("<StatusBar>", () => {
  let store: DaemonStore;
  beforeEach(() => {
    _resetDaemonRegistryForTests();
    store = createDaemonStore("d1", NOOP);
  });

  it("renders the connection segment with correct label and data attribute", () => {
    store.getState().setConnectionState("open");
    render(<StatusBar store={store} />);
    expect(
      screen.getByTestId("status-bar").getAttribute("data-connection"),
    ).toBe("open");
    expect(screen.getByTestId("status-bar-connection").textContent).toContain(
      "connected",
    );
  });

  it("renders an em-dash for latency when no value is provided", () => {
    render(<StatusBar store={store} latencyMs={null} />);
    expect(screen.getByTestId("status-bar-latency").textContent).toBe("— ms");
  });

  it("rounds latency to whole milliseconds", () => {
    render(<StatusBar store={store} latencyMs={42.7} />);
    expect(screen.getByTestId("status-bar-latency").textContent).toBe("43 ms");
  });

  it("aggregates cost across daemon sessions when sessionId is null", () => {
    store
      .getState()
      .hydrateSessions([
        mkSession({ id: "s-1", costUsd: 0.012 }),
        mkSession({ id: "s-2", costUsd: 0.4 }),
      ]);
    render(<StatusBar store={store} />);
    expect(screen.getByTestId("status-bar-cost").textContent).toBe("$0.4120");
  });

  it("scopes cost to the active session when sessionId is provided", () => {
    store
      .getState()
      .hydrateSessions([
        mkSession({ id: "s-1", costUsd: 0.012 }),
        mkSession({ id: "s-2", costUsd: 0.4 }),
      ]);
    render(<StatusBar store={store} sessionId="s-2" />);
    expect(screen.getByTestId("status-bar-cost").textContent).toBe("$0.4000");
  });

  it("counts running + waiting-tool lanes as running, failed as blocked", () => {
    store.getState().hydrateLanes("s-1", [
      mkLane({ id: "l1", state: "running" }),
      mkLane({ id: "l2", state: "waiting-tool" }),
      mkLane({ id: "l3", state: "completed" }),
      mkLane({ id: "l4", state: "failed" }),
    ]);
    render(<StatusBar store={store} sessionId="s-1" />);
    expect(screen.getByTestId("status-bar-lane-total").textContent).toBe("4 lanes");
    expect(screen.getByTestId("status-bar-lane-running").textContent).toBe(
      "2 running",
    );
    expect(screen.getByTestId("status-bar-lane-blocked").textContent).toContain(
      "1 blocked",
    );
  });

  it("hides the blocked chip when zero lanes have failed", () => {
    store.getState().hydrateLanes("s-1", [
      mkLane({ id: "l1", state: "running" }),
      mkLane({ id: "l2", state: "completed" }),
    ]);
    render(<StatusBar store={store} sessionId="s-1" />);
    expect(screen.queryByTestId("status-bar-lane-blocked")).toBeNull();
  });
});
