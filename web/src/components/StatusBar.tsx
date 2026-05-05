// SPDX-License-Identifier: MIT
// <StatusBar> — bottom-edge live telemetry strip. Spec item 38/55.
//
// Surfaces four status segments:
//   1. Connection state from the per-daemon ResilientSocket lifecycle
//      (idle / connecting / open / reconnecting / closed). Maps to a
//      colored dot + label.
//   2. WS round-trip latency in ms (when the heartbeat reports it).
//   3. Cumulative session cost in USD (formatted to 4 decimals).
//   4. Lane counts: total, running, blocked.
//
// All numeric segments accept null and render an em-dash when so. The
// bar is one fixed-height row so collapsing it doesn't reflow the
// chat surfaces above.
import type { ReactElement } from "react";
import { useStore } from "zustand";
import type { DaemonStore } from "@/lib/store/daemonStore";
import type { LaneState, SessionId } from "@/lib/api/types";
import { cn } from "@/lib/utils";

const CONN_DOT: Record<NonNullable<ReturnType<typeof connState>>, string> = {
  idle: "bg-slate-400",
  connecting: "bg-amber-500 animate-pulse",
  open: "bg-emerald-500",
  reconnecting: "bg-amber-500 animate-pulse",
  closed: "bg-rose-500",
};

const CONN_LABEL: Record<NonNullable<ReturnType<typeof connState>>, string> = {
  idle: "idle",
  connecting: "connecting",
  open: "connected",
  reconnecting: "reconnecting",
  closed: "disconnected",
};

function connState(
  s: ReturnType<DaemonStore["getState"]>,
): "idle" | "connecting" | "open" | "reconnecting" | "closed" {
  return s.ui.connectionState;
}

export interface StatusBarProps {
  store: DaemonStore;
  /** When provided, lane + cost segments scope to this session. When
   *  null, totals span the whole daemon. */
  sessionId?: SessionId | null;
  /** Latency in ms reported by the WS heartbeat. */
  latencyMs?: number | null;
}

function laneCounts(
  store: DaemonStore,
  sessionId: SessionId | null | undefined,
): { total: number; running: number; blocked: number } {
  const state = store.getState();
  const lanes = Object.values(state.lanes.byKey);
  const filtered = sessionId
    ? lanes.filter((l) => l.sessionId === sessionId)
    : lanes;
  let running = 0;
  let blocked = 0;
  for (const l of filtered) {
    if (l.state === ("running" satisfies LaneState)) running += 1;
    if (l.state === ("waiting-tool" satisfies LaneState)) running += 1;
    if (l.state === ("failed" satisfies LaneState)) blocked += 1;
  }
  return { total: filtered.length, running, blocked };
}

function sessionCost(
  store: DaemonStore,
  sessionId: SessionId | null | undefined,
): number | null {
  const state = store.getState();
  if (sessionId) {
    return state.sessions.byId[sessionId]?.costUsd ?? null;
  }
  let sum = 0;
  for (const s of Object.values(state.sessions.byId)) {
    sum += s.costUsd ?? 0;
  }
  return sum;
}

export function StatusBar({
  store,
  sessionId,
  latencyMs,
}: StatusBarProps): ReactElement {
  const conn = useStore(store, connState);
  // Subscribe to the slices that drive the counts/cost, so the status
  // bar updates live as those change. We don't need the slice values —
  // just the subscription — because we recompute via store.getState().
  useStore(store, (s) => s.lanes.byKey);
  useStore(store, (s) => s.sessions.byId);

  const counts = laneCounts(store, sessionId ?? null);
  const cost = sessionCost(store, sessionId ?? null);

  return (
    <div
      data-testid="status-bar"
      data-connection={conn}
      role="status"
      aria-label="Daemon status"
      className="flex items-center gap-4 px-3 h-7 text-xs border-t border-border bg-muted/40"
    >
      <span
        className="flex items-center gap-1"
        data-testid="status-bar-connection"
      >
        <span
          aria-hidden="true"
          className={cn(
            "inline-block w-2 h-2 rounded-full shrink-0",
            CONN_DOT[conn],
          )}
        />
        <span>{CONN_LABEL[conn]}</span>
      </span>

      <span data-testid="status-bar-latency">
        {typeof latencyMs === "number"
          ? `${Math.round(latencyMs)} ms`
          : "— ms"}
      </span>

      <span data-testid="status-bar-cost">
        {typeof cost === "number" ? `$${cost.toFixed(4)}` : "$—"}
      </span>

      <span className="ml-auto" />

      <span
        className="flex items-center gap-2"
        data-testid="status-bar-lanes"
      >
        <span data-testid="status-bar-lane-total">{counts.total} lanes</span>
        <span
          className="text-emerald-500"
          data-testid="status-bar-lane-running"
        >
          {counts.running} running
        </span>
        {counts.blocked > 0 ? (
          <span
            className="text-rose-500 font-semibold"
            data-testid="status-bar-lane-blocked"
          >
            {counts.blocked} blocked
          </span>
        ) : null}
      </span>
    </div>
  );
}
