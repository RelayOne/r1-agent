// SPDX-License-Identifier: MIT
// Sibling unit tests for sessionsSlice.ts — added to satisfy the
// coverage-manifest gate (every src/lib file needs a sibling test).
//
// Higher-level integration coverage (hydrate/markSubscribed/etc. composed
// into the full DaemonState) lives in daemonStore.test.tsx; these tests
// exercise the factory and pure helpers in isolation.
import { describe, it, expect } from "vitest";
import type { StoreApi } from "zustand";
import {
  createSessionsSlice,
  emptySessionsState,
  type SessionsSlice,
} from "@/lib/store/sessionsSlice";
import type { DaemonState } from "@/lib/store/daemonStore";

// Minimal StateCreator harness: a record-backed get/set that round-trips
// through a single mutable object. Sufficient for verifying the factory
// returns an action map shaped like SessionsSlice without dragging in
// the full daemonStore composition.
function harness(): {
  state: { current: Partial<DaemonState> };
  slice: SessionsSlice;
} {
  const state: { current: Partial<DaemonState> } = {
    current: { sessions: emptySessionsState() },
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
  const slice = createSessionsSlice(set, get, store);
  return { state, slice };
}

describe("sessionsSlice", () => {
  it("emptySessionsState returns a fresh empty record on every call", () => {
    const a = emptySessionsState();
    const b = emptySessionsState();
    expect(a.byId).toEqual({});
    expect(a.order).toEqual([]);
    expect(a.subscribed.size).toBe(0);
    expect(a.lastSeq).toEqual({});
    expect(a.errorBySession).toEqual({});
    expect(a).not.toBe(b); // distinct instances — no shared mutable state
  });

  it("createSessionsSlice returns the SessionsSlice action map", () => {
    const { slice } = harness();
    expect(typeof slice).toBe("object");
    expect(slice.sessions).toEqual(emptySessionsState());
    expect(typeof slice.hydrateSessions).toBe("function");
    expect(typeof slice.markSubscribed).toBe("function");
    expect(typeof slice.markUnsubscribed).toBe("function");
    expect(typeof slice.setSessionError).toBe("function");
  });
});
