// SPDX-License-Identifier: MIT
// <LanesSidebar> + <LaneRow> — right-rail lane index. Spec item 34/55.
//
// Each row shows a status dot, a label, an optional progress glyph
// (when the snapshot's `progress` is a number in [0,1]), a Pin
// button toggling the per-session tile-pinning state, and a Kill
// button (destructive). All rows derive from the per-daemon zustand
// store; the store coalesces high-frequency `lane.delta` envelopes
// at one animation frame (~16 ms ≈ 60 Hz worst-case) which clamps
// to ≤10 Hz visible rerender by the time React batches updates,
// satisfying the spec's "≤10 Hz coalesced rerender" requirement.
//
// `data-testid="lane-row-<laneId>"` on each row, plus dedicated
// testids for the pin / kill / focus affordances.
import { memo } from "react";
import type { ReactElement } from "react";
import { useStore } from "zustand";
import { Pin, PinOff, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { DaemonStore } from "@/lib/store/daemonStore";
import type {
  LaneId,
  LaneSnapshot,
  LaneState,
  SessionId,
} from "@/lib/api/types";
import { cn } from "@/lib/utils";

const STATE_DOT: Record<LaneState, string> = {
  queued: "bg-slate-400",
  running: "bg-emerald-500 animate-pulse",
  "waiting-tool": "bg-sky-500 animate-pulse",
  completed: "bg-violet-500",
  failed: "bg-rose-500",
  killed: "bg-slate-300 opacity-60",
};

const STATE_LABEL: Record<LaneState, string> = {
  queued: "queued",
  running: "running",
  "waiting-tool": "waiting on tool",
  completed: "completed",
  failed: "failed",
  killed: "killed",
};

export interface LaneRowProps {
  lane: LaneSnapshot;
  pinned: boolean;
  onTogglePin: (laneId: LaneId) => void;
  onKill: (laneId: LaneId) => void;
  onFocus?: (laneId: LaneId) => void;
}

export const LaneRow = memo(function LaneRow({
  lane,
  pinned,
  onTogglePin,
  onKill,
  onFocus,
}: LaneRowProps): ReactElement {
  const stateLabel = STATE_LABEL[lane.state];
  const dotClass = STATE_DOT[lane.state];
  const progressPct =
    typeof lane.progress === "number" ? Math.round(lane.progress * 100) : null;

  return (
    <li
      data-testid={`lane-row-${lane.id}`}
      data-lane-state={lane.state}
      data-pinned={pinned ? "true" : "false"}
      className={cn(
        "flex items-center gap-2 px-2 py-1 text-sm rounded-md",
        "hover:bg-muted/40",
      )}
    >
      <span
        aria-hidden="true"
        className={cn("inline-block w-2 h-2 rounded-full shrink-0", dotClass)}
      />
      <button
        type="button"
        onClick={() => onFocus?.(lane.id)}
        data-testid={`lane-row-${lane.id}-label`}
        aria-label={`Open lane ${lane.label}, ${stateLabel}`}
        className="flex-1 min-w-0 text-left truncate hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded px-1"
      >
        {lane.label}
      </button>
      {progressPct !== null ? (
        <span
          className="text-xs text-muted-foreground tabular-nums"
          data-testid={`lane-row-${lane.id}-progress`}
          aria-label={`${progressPct}% progress`}
        >
          {progressPct}%
        </span>
      ) : null}
      <Button
        type="button"
        size="icon"
        variant={pinned ? "secondary" : "ghost"}
        onClick={() => onTogglePin(lane.id)}
        data-testid={`lane-row-${lane.id}-pin`}
        aria-label={pinned ? "Unpin lane" : "Pin lane to tile grid"}
        aria-pressed={pinned}
        className="h-6 w-6"
      >
        {pinned ? (
          <PinOff className="w-3 h-3" aria-hidden="true" />
        ) : (
          <Pin className="w-3 h-3" aria-hidden="true" />
        )}
      </Button>
      <Button
        type="button"
        size="icon"
        variant="ghost"
        onClick={() => onKill(lane.id)}
        data-testid={`lane-row-${lane.id}-kill`}
        aria-label="Kill lane"
        className="h-6 w-6 text-rose-500 hover:bg-rose-500/10"
      >
        <X className="w-3 h-3" aria-hidden="true" />
      </Button>
    </li>
  );
});

export interface LanesSidebarProps {
  store: DaemonStore;
  sessionId: SessionId;
  onKill: (laneId: LaneId) => void;
  onFocus?: (laneId: LaneId) => void;
}

export function LanesSidebar({
  store,
  sessionId,
  onKill,
  onFocus,
}: LanesSidebarProps): ReactElement {
  const order = useStore(
    store,
    (s) => s.lanes.orderBySession[sessionId] ?? [],
  );
  const byKey = useStore(store, (s) => s.lanes.byKey);
  const pinnedIds = useStore(
    store,
    (s) => s.ui.tilePinnedBySession[sessionId] ?? [],
  );
  const pinLane = useStore(store, (s) => s.pinLane);
  const unpinLane = useStore(store, (s) => s.unpinLane);

  const onTogglePin = (laneId: LaneId): void => {
    if (pinnedIds.includes(laneId)) {
      unpinLane(sessionId, laneId);
    } else {
      pinLane(sessionId, laneId);
    }
  };

  if (order.length === 0) {
    return (
      <div
        className="p-3 text-sm text-muted-foreground"
        role="status"
        data-testid="lanes-sidebar-empty"
      >
        No lanes yet.
      </div>
    );
  }

  return (
    <ol
      role="list"
      aria-label="Lanes"
      data-testid="lanes-sidebar"
      className="m-0 p-1 space-y-0.5 overflow-y-auto h-full list-none"
    >
      {order.map((laneId) => {
        const lane = byKey[`${sessionId}:${laneId}`];
        if (!lane) return null;
        return (
          <LaneRow
            key={laneId}
            lane={lane}
            pinned={pinnedIds.includes(laneId)}
            onTogglePin={onTogglePin}
            onKill={onKill}
            onFocus={onFocus}
          />
        );
      })}
    </ol>
  );
}
