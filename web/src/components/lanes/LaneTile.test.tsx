// SPDX-License-Identifier: MIT
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, fireEvent, act } from "@testing-library/react";
import { LaneTile } from "@/components/lanes/LaneTile";
import {
  _resetDaemonRegistryForTests,
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";
import type { LaneSnapshot } from "@/lib/api/types";

const NOOP = { schedule: () => 0, cancel: () => {} };
const NOW = "2026-05-04T12:30:00.000Z";

function mkLane(over: Partial<LaneSnapshot> & { id: string }): LaneSnapshot {
  const { id, ...rest } = over;
  return {
    id,
    sessionId: "s-1",
    label: `lane ${id}`,
    state: "running",
    createdAt: NOW,
    updatedAt: NOW,
    progress: null,
    lastRender: "first frame",
    lastSeq: 0,
    ...rest,
  } as LaneSnapshot;
}

describe("<LaneTile>", () => {
  let store: DaemonStore;
  beforeEach(() => {
    _resetDaemonRegistryForTests();
    store = createDaemonStore("d1", NOOP);
  });

  it("renders header + body using the snapshot from the store", () => {
    store.getState().hydrateLanes("s-1", [
      mkLane({ id: "l1", label: "research", state: "waiting-tool", lastRender: "step 1\nstep 2" }),
    ]);
    render(
      <LaneTile
        store={store}
        sessionId="s-1"
        laneId="l1"
        onUnpin={() => {}}
        onKill={() => {}}
      />,
    );
    expect(screen.getByTestId("lane-tile-l1")).toBeTruthy();
    expect(screen.getByTestId("lane-tile-l1-label").textContent).toBe(
      "research",
    );
    expect(screen.getByTestId("lane-tile-l1-state").textContent).toBe(
      "waiting on tool",
    );
    expect(screen.getByTestId("lane-tile-l1-body").textContent).toContain(
      "step 1",
    );
  });

  it("renders the missing-state notice when the snapshot is absent", () => {
    render(
      <LaneTile
        store={store}
        sessionId="s-1"
        laneId="l-ghost"
        onUnpin={() => {}}
        onKill={() => {}}
      />,
    );
    expect(screen.getByTestId("lane-tile-l-ghost-missing")).toBeTruthy();
  });

  it("invokes onUnpin / onKill / onFocus with the lane id", () => {
    store.getState().hydrateLanes("s-1", [mkLane({ id: "l1" })]);
    const onUnpin = vi.fn();
    const onKill = vi.fn();
    const onFocus = vi.fn();
    render(
      <LaneTile
        store={store}
        sessionId="s-1"
        laneId="l1"
        onUnpin={onUnpin}
        onKill={onKill}
        onFocus={onFocus}
      />,
    );
    fireEvent.click(screen.getByTestId("lane-tile-l1-unpin"));
    fireEvent.click(screen.getByTestId("lane-tile-l1-kill"));
    fireEvent.click(screen.getByTestId("lane-tile-l1-focus"));
    expect(onUnpin).toHaveBeenCalledWith("l1");
    expect(onKill).toHaveBeenCalledWith("l1");
    expect(onFocus).toHaveBeenCalledWith("l1");
  });

  it("updates body when the store's lastRender for this lane mutates", () => {
    store.getState().hydrateLanes("s-1", [
      mkLane({ id: "l1", lastRender: "first" }),
    ]);
    render(
      <LaneTile
        store={store}
        sessionId="s-1"
        laneId="l1"
        onUnpin={() => {}}
        onKill={() => {}}
      />,
    );
    expect(screen.getByTestId("lane-tile-l1-body").textContent).toContain(
      "first",
    );
    act(() => {
      store.setState((prev) => ({
        ...prev,
        lanes: {
          ...prev.lanes,
          byKey: {
            ...prev.lanes.byKey,
            "s-1:l1": {
              ...prev.lanes.byKey["s-1:l1"],
              lastRender: "second frame",
            },
          },
        },
      }));
    });
    expect(screen.getByTestId("lane-tile-l1-body").textContent).toContain(
      "second frame",
    );
  });

  it("reflects state changes in data-lane-state and aria-label", () => {
    store.getState().hydrateLanes("s-1", [mkLane({ id: "l1", state: "running" })]);
    const { rerender } = render(
      <LaneTile
        store={store}
        sessionId="s-1"
        laneId="l1"
        onUnpin={() => {}}
        onKill={() => {}}
      />,
    );
    expect(
      screen.getByTestId("lane-tile-l1").getAttribute("data-lane-state"),
    ).toBe("running");
    act(() => {
      store.setState((prev) => ({
        ...prev,
        lanes: {
          ...prev.lanes,
          byKey: {
            ...prev.lanes.byKey,
            "s-1:l1": { ...prev.lanes.byKey["s-1:l1"], state: "completed" },
          },
        },
      }));
    });
    rerender(
      <LaneTile
        store={store}
        sessionId="s-1"
        laneId="l1"
        onUnpin={() => {}}
        onKill={() => {}}
      />,
    );
    expect(
      screen.getByTestId("lane-tile-l1").getAttribute("data-lane-state"),
    ).toBe("completed");
  });
});
