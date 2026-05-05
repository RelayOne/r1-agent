// SPDX-License-Identifier: MIT
// <LaneTile> — live tool-use output for one pinned lane. Spec item 35/55.
//
// Reads the lane snapshot from the per-daemon store and renders the
// cached `lastRender` string. The selector returns only the small
// per-lane subset (not the whole `byKey` map), so React's batching
// + zustand's shallow-equals strategy means this component re-renders
// only when this specific lane's render-string changes ("diff-only
// update" per the spec).
//
// The tile is composed of a header (label + state pill + unpin / kill
// affordances) and a scrollable body that renders the cached string
// inside a <pre>. We deliberately do NOT pipe the body through
// Streamdown / Shiki: the lane render-string is already a pre-baked
// terminal-style buffer (ANSI stripped on the daemon side per the
// lanes-protocol spec), so adding markdown parsing would mangle it.
//
// The ResizeObserver-based "scroll-to-bottom on growth" follows the
// same sticky-bottom rules as MessageLog (item 26): when the user has
// scrolled up, new content does not jerk the viewport.
import { memo, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { ReactElement } from "react";
import { useStore } from "zustand";
import { Pin, PinOff, X, Maximize2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { DaemonStore } from "@/lib/store/daemonStore";
import type {
  LaneId,
  LaneSnapshot,
  LaneState,
  SessionId,
} from "@/lib/api/types";
import { cn } from "@/lib/utils";

const STICK_THRESHOLD_PX = 32;

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

export interface LaneTileProps {
  store: DaemonStore;
  sessionId: SessionId;
  laneId: LaneId;
  onUnpin: (laneId: LaneId) => void;
  onKill: (laneId: LaneId) => void;
  onFocus?: (laneId: LaneId) => void;
}

export const LaneTile = memo(function LaneTile({
  store,
  sessionId,
  laneId,
  onUnpin,
  onKill,
  onFocus,
}: LaneTileProps): ReactElement | null {
  // Per-lane selector. Returns the same reference across renders that
  // didn't touch this lane's snapshot.
  const lane = useStore(
    store,
    (s): LaneSnapshot | undefined => s.lanes.byKey[`${sessionId}:${laneId}`],
  );

  const bodyRef = useRef<HTMLPreElement | null>(null);
  const [stuckBottom, setStuckBottom] = useState(true);

  useEffect(() => {
    const el = bodyRef.current;
    if (!el) return;
    const onScroll = (): void => {
      const dist = el.scrollHeight - el.scrollTop - el.clientHeight;
      setStuckBottom(dist <= STICK_THRESHOLD_PX);
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, []);

  useLayoutEffect(() => {
    if (!stuckBottom) return;
    const el = bodyRef.current;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
  }, [lane?.lastRender, stuckBottom]);

  if (!lane) {
    return (
      <section
        data-testid={`lane-tile-${laneId}-missing`}
        role="status"
        className="rounded-md border border-dashed border-border p-3 text-sm text-muted-foreground"
      >
        Lane {laneId} is not in this session.
      </section>
    );
  }

  const stateLabel = STATE_LABEL[lane.state];
  const dotClass = STATE_DOT[lane.state];

  return (
    <section
      data-testid={`lane-tile-${laneId}`}
      data-lane-state={lane.state}
      aria-label={`Lane ${lane.label}, ${stateLabel}`}
      className="rounded-md border border-border overflow-hidden flex flex-col h-full bg-background"
    >
      <header className="flex items-center gap-2 px-2 py-1.5 text-xs bg-muted/40">
        <span
          aria-hidden="true"
          className={cn("inline-block w-2 h-2 rounded-full shrink-0", dotClass)}
        />
        <span className="font-mono font-semibold truncate" data-testid={`lane-tile-${laneId}-label`}>
          {lane.label}
        </span>
        <span className="text-muted-foreground" data-testid={`lane-tile-${laneId}-state`}>
          {stateLabel}
        </span>
        {onFocus ? (
          <Button
            type="button"
            size="icon"
            variant="ghost"
            onClick={() => onFocus(laneId)}
            data-testid={`lane-tile-${laneId}-focus`}
            aria-label="Open focused lane view"
            className="ml-auto h-6 w-6"
          >
            <Maximize2 className="w-3 h-3" aria-hidden="true" />
          </Button>
        ) : (
          <span className="ml-auto" />
        )}
        <Button
          type="button"
          size="icon"
          variant="ghost"
          onClick={() => onUnpin(laneId)}
          data-testid={`lane-tile-${laneId}-unpin`}
          aria-label="Unpin lane"
          className="h-6 w-6"
        >
          <PinOff className="w-3 h-3" aria-hidden="true" />
        </Button>
        <Button
          type="button"
          size="icon"
          variant="ghost"
          onClick={() => onKill(laneId)}
          data-testid={`lane-tile-${laneId}-kill`}
          aria-label="Kill lane"
          className="h-6 w-6 text-rose-500 hover:bg-rose-500/10"
        >
          <X className="w-3 h-3" aria-hidden="true" />
        </Button>
      </header>
      <pre
        ref={bodyRef}
        data-testid={`lane-tile-${laneId}-body`}
        data-stuck-bottom={stuckBottom ? "true" : "false"}
        className="flex-1 m-0 p-2 overflow-auto font-mono text-xs whitespace-pre-wrap"
      >
        {lane.lastRender ?? ""}
      </pre>
    </section>
  );
});

// Re-export Pin for header / wiring symmetry.
export { Pin };
