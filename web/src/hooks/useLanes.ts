// SPDX-License-Identifier: MIT
// useLanes — selector hook that returns the ordered, filtered lane
// snapshot for one session. Used by LanesSidebar, LaneRow, LaneTile.
// Spec item 19/55 (one of four colocated hooks).
//
// Lane order is stable (creation timestamp + lane_id tiebreak). The
// store maintains `orderBySession[sessionId]`; this hook resolves
// that order against the snapshot map.
import { useMemo } from "react";
import { useStore } from "zustand";
import type { DaemonStore } from "@/lib/store/daemonStore";
import type { LaneSnapshot, SessionId } from "@/lib/api/types";

export interface UseLanesOptions {
  store: DaemonStore;
  sessionId: SessionId;
  /** Optional filter by state (e.g. hide completed). */
  filter?: (lane: LaneSnapshot) => boolean;
}

export interface UseLanesResult {
  /** Ordered lane snapshots for the session. */
  lanes: LaneSnapshot[];
  /** Pinned lane ids (in display order) for the TileGrid. */
  pinnedIds: string[];
  /** Per-lane collapse flags (key = lane_id). */
  collapsed: Record<string, boolean>;
}

export function useLanes(opts: UseLanesOptions): UseLanesResult {
  const { store, sessionId, filter } = opts;
  const order = useStore(store, (s) => s.lanes.orderBySession[sessionId]);
  const byKey = useStore(store, (s) => s.lanes.byKey);
  const pinnedIds = useStore(
    store,
    (s) => s.ui.tilePinnedBySession[sessionId] ?? EMPTY_LIST,
  );
  const collapsed = useStore(
    store,
    (s) => s.ui.tileCollapsedBySession[sessionId] ?? EMPTY_OBJECT,
  );

  const lanes = useMemo<LaneSnapshot[]>(() => {
    if (!order) return [];
    const out: LaneSnapshot[] = [];
    for (const lid of order) {
      const lane = byKey[`${sessionId}:${lid}`];
      if (!lane) continue;
      if (filter && !filter(lane)) continue;
      out.push(lane);
    }
    return out;
  }, [order, byKey, sessionId, filter]);

  return { lanes, pinnedIds, collapsed };
}

const EMPTY_LIST: string[] = [];
const EMPTY_OBJECT: Record<string, boolean> = {};
