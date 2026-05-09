// SPDX-License-Identifier: MIT
// Lanes slice — per spec §Directory Layout (item 16).
//
// Owns the `lanes` state record (lane snapshots keyed by
// `${sessionId}:${laneId}` plus per-session lane order) and the
// lane-related dispatchers, including the TileGrid pin / collapse /
// reorder operations that mutate the `ui` sub-state but are
// conceptually lane-shaped (lane lifecycle drives them).
import type { StateCreator } from "zustand";
import type { LaneId, LaneSnapshot, SessionId } from "@/lib/api/types";
import type { DaemonState } from "@/lib/store/daemonStore";

/** Internal shape of the `lanes` sub-state. */
export interface LanesState {
  /** Lanes keyed by `${sessionId}:${laneId}`; stored flat to keep
   *  per-session iteration cheap. */
  byKey: Record<string, LaneSnapshot>;
  /** Per-session lane order (stable: creation timestamp + lane_id tiebreak). */
  orderBySession: Record<SessionId, LaneId[]>;
}

export interface LanesSlice {
  lanes: LanesState;
  /** Bulk-load lanes for a session; replaces (does not merge) per-session order. */
  hydrateLanes: (sessionId: SessionId, lanes: LaneSnapshot[]) => void;
  /** Pin a lane onto the per-session TileGrid (mutates `ui.tilePinnedBySession`). */
  pinLane: (sessionId: SessionId, laneId: LaneId) => void;
  /** Unpin a lane from the per-session TileGrid; also clears its collapse flag. */
  unpinLane: (sessionId: SessionId, laneId: LaneId) => void;
  /** Replace the per-session TileGrid order. */
  reorderTiles: (sessionId: SessionId, ids: LaneId[]) => void;
  /** Toggle a tile's collapsed flag for a session. */
  toggleTileCollapsed: (sessionId: SessionId, laneId: LaneId) => void;
}

/** Internal helper used by the cross-slice envelope dispatcher in
 *  daemonStore.ts. Keeps the lane-key formatting in one place. */
export const laneKey = (sid: SessionId, lid: LaneId): string => `${sid}:${lid}`;

export function emptyLanesState(): LanesState {
  return { byKey: {}, orderBySession: {} };
}

export const createLanesSlice: StateCreator<
  DaemonState,
  [],
  [],
  LanesSlice
> = (set) => ({
  lanes: emptyLanesState(),

  hydrateLanes: (sessionId, lanesArr) =>
    set((prev) => {
      const byKey = { ...prev.lanes.byKey };
      const order: LaneId[] = [];
      for (const l of lanesArr) {
        byKey[laneKey(sessionId, l.id)] = l;
        order.push(l.id);
      }
      return {
        ...prev,
        lanes: {
          byKey,
          orderBySession: { ...prev.lanes.orderBySession, [sessionId]: order },
        },
      };
    }),

  pinLane: (sessionId, laneId) =>
    set((prev) => {
      const cur = prev.ui.tilePinnedBySession[sessionId] ?? [];
      if (cur.includes(laneId)) return prev;
      return {
        ...prev,
        ui: {
          ...prev.ui,
          tilePinnedBySession: {
            ...prev.ui.tilePinnedBySession,
            [sessionId]: [...cur, laneId],
          },
        },
      };
    }),

  unpinLane: (sessionId, laneId) =>
    set((prev) => {
      const cur = prev.ui.tilePinnedBySession[sessionId] ?? [];
      const next = cur.filter((x) => x !== laneId);
      const collapsedCur = prev.ui.tileCollapsedBySession[sessionId] ?? {};
      const { [laneId]: _drop, ...collapsedNext } = collapsedCur;
      void _drop;
      return {
        ...prev,
        ui: {
          ...prev.ui,
          tilePinnedBySession: {
            ...prev.ui.tilePinnedBySession,
            [sessionId]: next,
          },
          tileCollapsedBySession: {
            ...prev.ui.tileCollapsedBySession,
            [sessionId]: collapsedNext,
          },
        },
      };
    }),

  reorderTiles: (sessionId, ids) =>
    set((prev) => ({
      ...prev,
      ui: {
        ...prev.ui,
        tilePinnedBySession: { ...prev.ui.tilePinnedBySession, [sessionId]: ids },
      },
    })),

  toggleTileCollapsed: (sessionId, laneId) =>
    set((prev) => {
      const cur = prev.ui.tileCollapsedBySession[sessionId] ?? {};
      return {
        ...prev,
        ui: {
          ...prev.ui,
          tileCollapsedBySession: {
            ...prev.ui.tileCollapsedBySession,
            [sessionId]: { ...cur, [laneId]: !cur[laneId] },
          },
        },
      };
    }),
});
