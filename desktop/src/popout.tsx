// SPDX-License-Identifier: MIT
//
// Pop-out window entry — mounted inside the WebviewWindow created by
// `popout::open_or_focus_lane_popout` (item 23).
//
// Per spec desktop-cortex-augmentation §6.1 + checklist item 24, the
// pop-out URL carries `?popout=lane&session=<>&lane=<>`; this entry
// reads those params, mounts `<PoppedLaneApp>` from
// `@r1/web-components`, and threads a Tauri-Channel-backed
// subscribe() so the focus view receives lane events the same way
// the inline LaneSidebar does.
//
// One pop-out per (session, lane). The PoppedLaneApp wires its own
// kill / copy-link buttons; both delegate back to the host via
// invoke().

import * as React from "react";
import { createRoot } from "react-dom/client";
import { invoke } from "@tauri-apps/api/core";
import {
  PoppedLaneApp,
  type LaneSubscribeFn,
} from "@r1/web-components";

import { subscribeLanes } from "./lib/laneSubscription";

// ---------------------------------------------------------------------------
// URL param parsing
// ---------------------------------------------------------------------------

interface PopoutParams {
  sessionId: string;
  laneId: string;
  initialTitle: string;
}

export function parsePopoutParams(search: string): PopoutParams | null {
  const u = new URLSearchParams(search.startsWith("?") ? search.slice(1) : search);
  if (u.get("popout") !== "lane") return null;
  const sessionId = u.get("session") ?? "";
  const laneId = u.get("lane") ?? "";
  const title = u.get("title") ?? `Lane ${laneId || "(unknown)"}`;
  if (!sessionId || !laneId) return null;
  return { sessionId, laneId, initialTitle: title };
}

// ---------------------------------------------------------------------------
// Subscribe wrapper — adapts subscribeLanes() from session-multiplex
// to the single-lane LaneSubscribeFn the PoppedLaneApp expects.
// ---------------------------------------------------------------------------

const popoutSubscribe: LaneSubscribeFn = async (
  sessionId,
  laneId,
  onEvent,
) => {
  // The host forwarder multiplexes; in the pop-out we only care
  // about events for this lane.
  return subscribeLanes(sessionId, (ev) => {
    if (ev.session_id !== sessionId) return;
    if ("lane_id" in ev && ev.lane_id !== laneId) return;
    onEvent(ev);
  });
};

// ---------------------------------------------------------------------------
// Mount entry
// ---------------------------------------------------------------------------

export function mountPopout(container: HTMLElement, search: string): boolean {
  const params = parsePopoutParams(search);
  if (!params) return false;

  const root = createRoot(container);
  root.render(
    React.createElement(PoppedLaneApp, {
      sessionId: params.sessionId,
      laneId: params.laneId,
      initialTitle: params.initialTitle,
      subscribe: popoutSubscribe,
      onCopyLink: (deeplink: string) => {
        // Best-effort clipboard write. The dev clipboard plugin
        // isn't enabled in scaffold mode; navigator.clipboard works
        // inside the WebView once the right permission is granted
        // by capabilities/default.json. We swallow errors so a
        // missing permission never crashes the pop-out.
        if (typeof navigator !== "undefined" && navigator.clipboard) {
          void navigator.clipboard.writeText(deeplink).catch(() => undefined);
        }
      },
      onKill: (laneId: string) => {
        void invoke("session_lanes_kill", {
          params: { session_id: params.sessionId, lane_id: laneId },
        }).catch(() => undefined);
      },
    }),
  );

  return true;
}

// ---------------------------------------------------------------------------
// Auto-bootstrap when this module is loaded as the root entry.
// (The main bundle calls mountPopout itself; this guard fires only
// when the WebView opens popout.tsx directly via index.html?popout=…)
// ---------------------------------------------------------------------------

if (typeof window !== "undefined") {
  const params = parsePopoutParams(window.location.search);
  if (params) {
    document.title = params.initialTitle;
    const target =
      document.getElementById("root") ?? document.body.appendChild(
        Object.assign(document.createElement("div"), { id: "root" }),
      );
    mountPopout(target, window.location.search);
  }
}
