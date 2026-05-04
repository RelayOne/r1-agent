// SPDX-License-Identifier: MIT
//
// Lane rail mount — bridges the React @r1/web-components LaneSidebar
// into the imperative HTMLElement panel surface.
//
// Per spec desktop-cortex-augmentation §8 + checklist item 22, the
// session-view panel grows a right-rail aside that renders the
// shared LaneSidebar from @r1/web-components. Because the rest of
// session-view.ts is plain TS / DOM, we mount React lazily into the
// supplied container and expose a tiny imperative handle:
//
//   handle.attach(sessionId)   Re-bind the rail to the named
//                              session; pass null to detach.
//   handle.dispose()           Tear down the React root + any live
//                              subscription; safe to call twice.
//
// Subscription lifecycle: each `attach(sessionId)` opens a fresh
// laneSubscription and tears down the previous one. Subscription
// teardown is awaited so the host LanesState entry is gone before
// the next attach registers a new id.
//
// Lane state itself (the items[] feeding LaneSidebar) is updated
// reactively from incoming LaneEvents inside a small reducer, so
// the host caller doesn't need to thread state through.

import * as React from "react";
import { createRoot, type Root } from "react-dom/client";
import {
  LaneSidebar,
  type LaneSidebarItem,
  type LaneEvent,
  type LaneStatus,
} from "@r1/web-components";

import { subscribeLanes, type LaneUnsubscribe } from "../lib/laneSubscription";

// ---------------------------------------------------------------------------
// Public handle
// ---------------------------------------------------------------------------

export interface LaneRailHandle {
  /** Re-bind the rail to the given session, or detach when null. */
  attach(sessionId: string | null): void;
  /** Render explicit items (e.g. from a session.lanes.list response). */
  setItems(items: LaneSidebarItem[]): void;
  /** Tear down React root + live subscription. Idempotent. */
  dispose(): void;
}

interface RailState {
  sessionId: string | null;
  items: Map<string, LaneSidebarItem>;
  selected: string | null;
  density: "verbose" | "normal" | "summary";
}

// ---------------------------------------------------------------------------
// Reducer — apply one LaneEvent to the items map
// ---------------------------------------------------------------------------

function applyEvent(
  state: RailState,
  ev: LaneEvent,
): { changed: boolean; state: RailState } {
  // Only process events for the currently-attached session; events
  // for other sessions slip in only if the host's forwarder
  // multiplexes (it shouldn't, per spec §8 — one channel per
  // session — but we guard anyway).
  if (state.sessionId !== ev.session_id) {
    return { changed: false, state };
  }

  const next = new Map(state.items);
  let changed = false;

  switch (ev.kind) {
    case "spawned": {
      next.set(ev.lane_id, {
        laneId: ev.lane_id,
        title: ev.title,
        status: "running",
        createdAt: ev.at,
        lastEventPreview: undefined,
      });
      changed = true;
      break;
    }
    case "status": {
      const cur = next.get(ev.lane_id);
      if (cur) {
        next.set(ev.lane_id, { ...cur, status: ev.to as LaneStatus });
        changed = true;
      }
      break;
    }
    case "killed": {
      const cur = next.get(ev.lane_id);
      if (cur) {
        next.set(ev.lane_id, {
          ...cur,
          status: "cancelled",
          lastEventPreview: ev.reason,
        });
        changed = true;
      }
      break;
    }
    case "delta": {
      const cur = next.get(ev.lane_id);
      if (cur) {
        const preview = describeDeltaPreview(ev.payload);
        if (preview && cur.lastEventPreview !== preview) {
          next.set(ev.lane_id, { ...cur, lastEventPreview: preview });
          changed = true;
        }
      }
      break;
    }
    case "delta_gap": {
      // Visual marker; we keep the item but don't reshuffle order.
      const cur = next.get(ev.lane_id);
      if (cur) {
        next.set(ev.lane_id, {
          ...cur,
          lastEventPreview: `gap @ seq ${ev.last_seen_seq}`,
        });
        changed = true;
      }
      break;
    }
  }

  if (!changed) return { changed, state };
  return { changed: true, state: { ...state, items: next } };
}

function describeDeltaPreview(
  payload: LaneEvent extends { kind: "delta" } ? unknown : never,
): string {
  // payload shape is per LaneDeltaPayload — `{kind, ...}`. Surface a
  // short human label for the rail; LaneDetail (focus view) renders
  // the full content.
  if (payload && typeof payload === "object" && "kind" in payload) {
    const k = String((payload as { kind?: unknown }).kind);
    return k;
  }
  return "";
}

// ---------------------------------------------------------------------------
// mountLaneRail
// ---------------------------------------------------------------------------

export function mountLaneRail(container: HTMLElement): LaneRailHandle {
  const root: Root = createRoot(container);

  let state: RailState = {
    sessionId: null,
    items: new Map(),
    selected: null,
    density: "normal",
  };
  let unsub: LaneUnsubscribe | null = null;
  let disposed = false;

  function render(): void {
    const items = Array.from(state.items.values());
    root.render(
      React.createElement(LaneSidebar, {
        items,
        selectedLaneId: state.selected,
        density: state.density,
        emptyMessage: state.sessionId
          ? "No lanes yet."
          : "No active session.",
        onSelectLane: (laneId: string) => {
          state = { ...state, selected: laneId };
          render();
        },
      }),
    );
  }

  async function teardown(): Promise<void> {
    const u = unsub;
    unsub = null;
    if (u) await u();
  }

  function attach(sessionId: string | null): void {
    if (disposed) return;
    if (sessionId === state.sessionId) return;
    // Detach previous subscription.
    void teardown();
    state = {
      ...state,
      sessionId,
      items: new Map(),
      selected: null,
    };
    render();
    if (!sessionId) return;

    subscribeLanes(sessionId, (ev) => {
      const { changed, state: nextState } = applyEvent(state, ev);
      if (!changed) return;
      state = nextState;
      render();
    })
      .then((teardownFn) => {
        if (disposed || state.sessionId !== sessionId) {
          void teardownFn();
          return;
        }
        unsub = teardownFn;
      })
      .catch((_err) => {
        // Swallow; the laneSubscription helper synthesises a
        // killed-event into the consumer on hard subscribe failure.
      });
  }

  function setItems(items: LaneSidebarItem[]): void {
    if (disposed) return;
    const map = new Map<string, LaneSidebarItem>();
    for (const it of items) map.set(it.laneId, it);
    state = { ...state, items: map };
    render();
  }

  function dispose(): void {
    if (disposed) return;
    disposed = true;
    void teardown();
    root.unmount();
  }

  // First render: detached state.
  render();

  return { attach, setItems, dispose };
}
