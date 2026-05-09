// SPDX-License-Identifier: MIT
// Sibling unit tests for lanesSlice.ts — added to satisfy the
// coverage-manifest gate (every src/lib file needs a sibling test).
//
// End-to-end behaviour (pinLane / reorderTiles / toggleTileCollapsed
// composed into the full DaemonState including the `ui` slice) is
// covered in daemonStore.test.tsx; these tests pin the factory shape
// and the pure helpers in isolation.
import { describe, it, expect } from "vitest";
import type { StoreApi } from "zustand";
import {
  createLanesSlice,
  emptyLanesState,
  laneKey,
  type LanesSlice,
} from "@/lib/store/lanesSlice";
import type { DaemonState } from "@/lib/store/daemonStore";

function harness(): {
  state: { current: Partial<DaemonState> };
  slice: LanesSlice;
} {
  const state: { current: Partial<DaemonState> } = {
    current: { lanes: emptyLanesState() },
  };
  const set: StoreApi<DaemonState>["setState"] = ((
    updater: unknown,
  ): void => {
    if (typeof updater === "function") {
      const next = (updater as (prev: DaemonState) => Partial<DaemonState>)(
        state.current as DaemonState,
      );
      state.current = { ...state.current, ...next } as Partial<DaemonState>;
      return;
    }
    state.current = { ...state.current, ...(updater as Partial<DaemonState>) };
  }) as StoreApi<DaemonState>["setState"];
  const get = (() => state.current as DaemonState) as StoreApi<DaemonState>["getState"];
  const store = {} as StoreApi<DaemonState>;
  const slice = createLanesSlice(set, get, store);
  return { state, slice };
}

describe("lanesSlice", () => {
  it("laneKey concatenates session and lane ids with a colon", () => {
    expect(laneKey("s1", "L1")).toBe("s1:L1");
    expect(laneKey("session-abc", "lane-xyz")).toBe("session-abc:lane-xyz");
  });

  it("emptyLanesState returns a fresh empty record on every call", () => {
    const a = emptyLanesState();
    const b = emptyLanesState();
    expect(a.byKey).toEqual({});
    expect(a.orderBySession).toEqual({});
    expect(a).not.toBe(b);
  });

  it("createLanesSlice returns the LanesSlice action map", () => {
    const { slice } = harness();
    expect(typeof slice).toBe("object");
    expect(slice.lanes).toEqual(emptyLanesState());
    expect(typeof slice.hydrateLanes).toBe("function");
    expect(typeof slice.pinLane).toBe("function");
    expect(typeof slice.unpinLane).toBe("function");
    expect(typeof slice.reorderTiles).toBe("function");
    expect(typeof slice.toggleTileCollapsed).toBe("function");
  });
});
