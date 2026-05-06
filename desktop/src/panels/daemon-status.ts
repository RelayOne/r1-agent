// SPDX-License-Identifier: MIT
//
// DaemonStatus title-bar pill — spec desktop-cortex-augmentation §5
// + checklist item 26.
//
// Renders a four-state pill in the app's title bar that reflects the
// transport's lifecycle. Listens for `daemon.up` / `daemon.down`
// events on Tauri's global event bus (emitted from transport.rs's
// run-loop). Click navigates Settings → Daemon (the consumer wires
// the navigation callback because routing differs across panels).
//
// State model mirrors the spec banner palette:
//
//   Connected (external)   green dot   external r1 serve daemon attached
//   Bundled daemon         blue dot    sidecar fallback live
//   Reconnecting…          yellow dot  during retry
//   Offline                red dot     hard fail; click to retry
//
// The component is plain DOM (matches the style of other
// desktop/src/panels/*.ts files); no React mount overhead because
// the pill is a single span.

import { listen, type UnlistenFn } from "@tauri-apps/api/event";

// -------------------------------------------------------------------
// Types
// -------------------------------------------------------------------

export type DaemonState =
  | { kind: "external"; url: string }
  | { kind: "sidecar"; url: string }
  | { kind: "reconnecting"; url: string; attempt: number; nextInMs: number }
  | { kind: "offline"; url: string; reason: string };

export interface DaemonStatusProps {
  /** Optional click target: opens Settings → Daemon. */
  onClick?: () => void;
  /** Optional click target when in `offline` state: trigger reconnect. */
  onRetry?: () => Promise<void>;
}

export interface DaemonStatusHandle {
  /** Force-update the pill (e.g. after manual probe). */
  set(state: DaemonState): void;
  /** Read the current state synchronously. */
  current(): DaemonState;
  /** Tear down the listeners + DOM listeners. Idempotent. */
  dispose(): void;
}

// -------------------------------------------------------------------
// Pill renderer (pure: state → markup) — testable in isolation
// -------------------------------------------------------------------

interface PillRender {
  className: string;
  label: string;
  title: string;
  ariaLabel: string;
}

export function renderPill(state: DaemonState): PillRender {
  switch (state.kind) {
    case "external":
      return {
        className: "r1-daemon-pill r1-daemon-pill--external",
        label: "Connected (external)",
        title: `External daemon at ${state.url}`,
        ariaLabel: `Daemon connected: external at ${state.url}`,
      };
    case "sidecar":
      return {
        className: "r1-daemon-pill r1-daemon-pill--sidecar",
        label: "Bundled daemon",
        title: `Sidecar daemon at ${state.url}`,
        ariaLabel: `Daemon connected: bundled sidecar at ${state.url}`,
      };
    case "reconnecting":
      return {
        className: "r1-daemon-pill r1-daemon-pill--reconnecting",
        label: "Reconnecting…",
        title: `Reconnecting to ${state.url} (attempt ${state.attempt})`,
        ariaLabel: `Daemon reconnecting (attempt ${state.attempt}, next try in ${Math.round(state.nextInMs / 100) / 10}s)`,
      };
    case "offline":
      return {
        className: "r1-daemon-pill r1-daemon-pill--offline",
        label: "Offline",
        title: `Daemon offline: ${state.reason}`,
        ariaLabel: `Daemon offline: ${state.reason}. Click to retry.`,
      };
  }
}

// -------------------------------------------------------------------
// Mount entry
// -------------------------------------------------------------------

const DEFAULT_STATE: DaemonState = {
  kind: "offline",
  url: "",
  reason: "starting",
};

interface DaemonUpPayload {
  url: string;
  mode: "external" | "sidecar";
  at?: string;
  replayed_from?: string;
}

interface DaemonDownPayload {
  reason: string;
  at?: string;
  will_retry?: boolean;
}

export async function mountDaemonStatus(
  container: HTMLElement,
  props: DaemonStatusProps = {},
): Promise<DaemonStatusHandle> {
  let state: DaemonState = DEFAULT_STATE;
  let disposed = false;

  container.classList.add("r1-daemon-pill-container");
  container.setAttribute("role", "status");
  const dot = document.createElement("span");
  dot.className = "r1-daemon-pill__dot";
  dot.setAttribute("aria-hidden", "true");
  const label = document.createElement("span");
  label.className = "r1-daemon-pill__label";
  container.replaceChildren(dot, label);

  function paint(): void {
    const r = renderPill(state);
    container.className = r.className;
    container.title = r.title;
    container.setAttribute("aria-label", r.ariaLabel);
    label.textContent = r.label;
  }
  paint();

  const handleClick = () => {
    if (state.kind === "offline" && props.onRetry) {
      void props.onRetry();
      return;
    }
    if (props.onClick) props.onClick();
  };
  container.addEventListener("click", handleClick);

  // Listen for daemon.up + daemon.down on the global Tauri event bus.
  const unlistens: UnlistenFn[] = [];

  const upUnlisten = await listen<DaemonUpPayload>("daemon.up", (ev) => {
    if (disposed) return;
    const payload = ev.payload;
    state =
      payload.mode === "sidecar"
        ? { kind: "sidecar", url: payload.url }
        : { kind: "external", url: payload.url };
    paint();
  });
  unlistens.push(upUnlisten);

  const downUnlisten = await listen<DaemonDownPayload>("daemon.down", (ev) => {
    if (disposed) return;
    const payload = ev.payload;
    if (payload.will_retry) {
      // Promote to reconnecting for the duration; the run-loop emits
      // the lifecycle ticks via its own channel (transport.rs
      // LifecycleEvent), but daemon.down is the moment we know the
      // socket dropped. attempt / next_in_ms get filled by the
      // lifecycle stream listener once that lands.
      state = {
        kind: "reconnecting",
        url: state.kind === "offline" ? "" : urlOf(state),
        attempt: 0,
        nextInMs: 250,
      };
    } else {
      state = {
        kind: "offline",
        url: state.kind === "offline" ? "" : urlOf(state),
        reason: payload.reason,
      };
    }
    paint();
  });
  unlistens.push(downUnlisten);

  return {
    set(next) {
      if (disposed) return;
      state = next;
      paint();
    },
    current() {
      return state;
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      container.removeEventListener("click", handleClick);
      for (const u of unlistens) {
        try {
          u();
        } catch {
          // listener teardown errors are fine; we're disposing anyway.
        }
      }
    },
  };
}

function urlOf(state: DaemonState): string {
  switch (state.kind) {
    case "external":
    case "sidecar":
    case "reconnecting":
    case "offline":
      return state.url;
  }
}
