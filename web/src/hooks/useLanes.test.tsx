// SPDX-License-Identifier: MIT
import React from "react";
import { describe, it, expect, beforeEach } from "vitest";
import { render } from "@testing-library/react";
import { useLanes, type UseLanesResult } from "@/hooks/useLanes";
import {
  _resetDaemonRegistryForTests,
  createDaemonStore,
  type DaemonStore,
} from "@/lib/store/daemonStore";
import type { LaneSnapshot } from "@/lib/api/types";

const NOW = "2026-05-04T12:00:00.000Z";
const NOOP = { schedule: () => 0, cancel: () => {} };

function lane(id: string, sid: string, state: LaneSnapshot["state"] = "running"): LaneSnapshot {
  return {
    id,
    sessionId: sid,
    label: `lane-${id}`,
    state,
    createdAt: NOW,
    updatedAt: NOW,
    progress: null,
    lastRender: null,
    lastSeq: 0,
  };
}

function Host({ store, sessionId, apiRef }: {
  store: DaemonStore;
  sessionId: string;
  apiRef: { current: UseLanesResult | null };
}): React.ReactElement {
  const r = useLanes({ store, sessionId });
  apiRef.current = r;
  return <div />;
}

describe("useLanes", () => {
  beforeEach(() => _resetDaemonRegistryForTests());

  it("returns empty arrays for an unknown session", () => {
    const store = createDaemonStore("d1", NOOP);
    const apiRef = { current: null as UseLanesResult | null };
    render(<Host store={store} sessionId="missing" apiRef={apiRef} />);
    expect(apiRef.current?.lanes).toEqual([]);
    expect(apiRef.current?.pinnedIds).toEqual([]);
    expect(apiRef.current?.collapsed).toEqual({});
  });

  it("returns ordered lanes for a session", () => {
    const store = createDaemonStore("d1", NOOP);
    store.getState().hydrateLanes("s1", [lane("L1", "s1"), lane("L2", "s1")]);
    const apiRef = { current: null as UseLanesResult | null };
    render(<Host store={store} sessionId="s1" apiRef={apiRef} />);
    expect(apiRef.current?.lanes.map((l) => l.id)).toEqual(["L1", "L2"]);
  });

  it("filter callback prunes lanes from the result", () => {
    const store = createDaemonStore("d1", NOOP);
    store.getState().hydrateLanes("s1", [
      lane("L1", "s1", "running"),
      lane("L2", "s1", "completed"),
      lane("L3", "s1", "running"),
    ]);
    const apiRef = { current: null as UseLanesResult | null };
    function H(): React.ReactElement {
      const r = useLanes({ store, sessionId: "s1", filter: (l) => l.state !== "completed" });
      apiRef.current = r;
      return <div />;
    }
    render(<H />);
    expect(apiRef.current?.lanes.map((l) => l.id)).toEqual(["L1", "L3"]);
  });

  it("exposes pinnedIds and per-lane collapsed flags", () => {
    const store = createDaemonStore("d1", NOOP);
    store.getState().hydrateLanes("s1", [lane("L1", "s1")]);
    store.getState().pinLane("s1", "L1");
    store.getState().toggleTileCollapsed("s1", "L1");
    const apiRef = { current: null as UseLanesResult | null };
    render(<Host store={store} sessionId="s1" apiRef={apiRef} />);
    expect(apiRef.current?.pinnedIds).toEqual(["L1"]);
    expect(apiRef.current?.collapsed.L1).toStrictEqual(true);
  });
});
