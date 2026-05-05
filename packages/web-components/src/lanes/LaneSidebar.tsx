// LaneSidebar — ordered list of LaneCards for one session.
//
// Stable creation-time ordering: lanes never re-shuffle when their status
// changes (RT-SURFACES "lane ordering churn"). Sort key is `created_at`
// ascending, lane_id as deterministic tiebreaker.
//
// Diff-only repaint: each LaneCard is React.memo'd; the sidebar passes
// the smallest stable prop set per card so React re-renders only the
// cards whose props actually changed. Empty-buffer lanes never re-render
// even when sibling lanes flush deltas (D-S2 perf gate).

import * as React from "react";
import type { LaneStatus } from "../types/LaneEvent";
import { LaneCard } from "./LaneCard";

export interface LaneSidebarItem {
  laneId: string;
  title: string;
  status: LaneStatus;
  createdAt: string; // ISO 8601
  lastEventPreview?: string;
}

export interface LaneSidebarProps {
  items: LaneSidebarItem[];
  selectedLaneId?: string | null;
  onSelectLane?: (laneId: string) => void;
  onKillLane?: (laneId: string) => void;
  density?: "verbose" | "normal" | "summary";
  emptyMessage?: string;
}

export const LaneSidebar: React.FC<LaneSidebarProps> = function LaneSidebar(
  props: LaneSidebarProps,
) {
  const {
    items,
    selectedLaneId,
    onSelectLane,
    onKillLane,
    density = "normal",
    emptyMessage = "No lanes yet.",
  } = props;

  // Sort by createdAt ascending; lane_id breaks ties for determinism.
  // Memoised so unchanged item refs don't trigger re-sort + re-render.
  const ordered = React.useMemo(() => {
    const copy = items.slice();
    copy.sort((a, b) => {
      if (a.createdAt < b.createdAt) return -1;
      if (a.createdAt > b.createdAt) return 1;
      if (a.laneId < b.laneId) return -1;
      if (a.laneId > b.laneId) return 1;
      return 0;
    });
    return copy;
  }, [items]);

  if (ordered.length === 0) {
    return (
      <aside
        className={`lane-sidebar lane-sidebar--density-${density} lane-sidebar--empty`}
        role="list"
        aria-label="cognition lanes"
      >
        <div className="lane-sidebar__empty">{emptyMessage}</div>
      </aside>
    );
  }

  return (
    <aside
      className={`lane-sidebar lane-sidebar--density-${density}`}
      role="list"
      aria-label="cognition lanes"
    >
      {ordered.map((it) => (
        <LaneCard
          key={it.laneId}
          laneId={it.laneId}
          title={it.title}
          status={it.status}
          lastEventPreview={
            density === "summary" ? undefined : it.lastEventPreview
          }
          selected={selectedLaneId === it.laneId}
          onSelect={onSelectLane}
          onKill={onKillLane}
        />
      ))}
    </aside>
  );
};
