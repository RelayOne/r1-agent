// SPDX-License-Identifier: MIT
//
// Lane subscription wrapper — TS half of the bridge to lanes.rs (item 17).
//
// Per spec desktop-cortex-augmentation §8: opens a Tauri
// `Channel<LaneEvent>`, hands the handle to the host via
// `session_lanes_subscribe`, and returns a teardown closure that
// unsubscribes when the consumer (component / pop-out) unmounts.
//
// The forwarder on the Rust side multiplexes every lane in the
// session over this single channel; the consumer narrows on
// `event.kind` (matches the type guards exported by
// @r1/web-components types/LaneEvent).
//
// React integration helper `useLaneSubscription` wires the lifecycle
// to a useEffect cleanup so dropping a component automatically tears
// the subscription down — closing both the Channel and the
// host-side LanesState entry.

import { Channel, invoke } from "@tauri-apps/api/core";
import type { LaneEvent } from "@r1/web-components";
import * as React from "react";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type LaneEventHandler = (ev: LaneEvent) => void;

export type LaneUnsubscribe = () => Promise<void>;

interface SubscribeReturn {
  subscription_id: string;
}

// ---------------------------------------------------------------------------
// subscribeLanes — imperative API
// ---------------------------------------------------------------------------

/**
 * Open a per-session lane subscription. Returns a teardown closure
 * the caller MUST invoke when done; the closure is idempotent so
 * double-call is safe.
 *
 * Failure modes:
 *
 * - The host returns a `not_found` taxonomy error if the session is
 *   unknown -- the closure is a no-op and the rejected promise
 *   surfaces verbatim.
 * - The Channel may be dropped server-side if the WebView pauses
 *   too long (D-S2 backpressure rules). The host emits a synthetic
 *   `delta_gap` event so the consumer can re-fetch via
 *   `session.lanes.list`.
 */
export async function subscribeLanes(
  sessionId: string,
  onEvent: LaneEventHandler,
): Promise<LaneUnsubscribe> {
  const ch = new Channel<LaneEvent>();
  ch.onmessage = onEvent;

  const { subscription_id } = await invoke<SubscribeReturn>(
    "session_lanes_subscribe",
    {
      params: { session_id: sessionId },
      // Tauri's Channel handle is passed as a top-level kwarg, not
      // inside `params`, so the macro can rewire it during dispatch.
      on_event: ch,
    },
  );

  let torn = false;
  return async () => {
    if (torn) return;
    torn = true;
    try {
      await invoke("session_lanes_unsubscribe", {
        params: { subscription_id, session_id: sessionId },
      });
    } catch (err) {
      // The host may have already dropped the subscription
      // (e.g. after a daemon disconnect emitted lane.killed for
      // every lane). Swallow not_found so unmount paths don't
      // surface noise; rethrow other errors.
      const stoke = (err as { stoke_code?: string } | null)?.stoke_code;
      if (stoke !== "not_found") throw err;
    }
  };
}

// ---------------------------------------------------------------------------
// React hook — auto-cleanup on unmount
// ---------------------------------------------------------------------------

export interface UseLaneSubscriptionOptions {
  /** Disable the subscription without unmounting (e.g. while a session is paused). */
  enabled?: boolean;
}

export interface UseLaneSubscriptionResult {
  /** Synchronous accessor for the latest event count (debug-friendly). */
  eventsReceived: number;
  /** True between subscribe() resolving and the unmount cleanup running. */
  active: boolean;
}

/**
 * useLaneSubscription — opens a subscription on mount, tears down on
 * unmount or when `sessionId` / `enabled` changes.
 *
 * The consumer passes a stable `onEvent` callback (use
 * `useCallback`); changes to that callback ref do NOT re-subscribe
 * because the hook routes events through a stable forwarder.
 */
export function useLaneSubscription(
  sessionId: string | null,
  onEvent: LaneEventHandler,
  opts: UseLaneSubscriptionOptions = {},
): UseLaneSubscriptionResult {
  const [active, setActive] = React.useState(false);
  const [eventsReceived, setEventsReceived] = React.useState(0);
  const handlerRef = React.useRef(onEvent);
  React.useEffect(() => {
    handlerRef.current = onEvent;
  }, [onEvent]);

  const enabled = opts.enabled !== false;

  React.useEffect(() => {
    if (!sessionId || !enabled) return;
    let unsub: LaneUnsubscribe | null = null;
    let cancelled = false;

    const stableForwarder: LaneEventHandler = (ev) => {
      setEventsReceived((n) => n + 1);
      handlerRef.current(ev);
    };

    subscribeLanes(sessionId, stableForwarder)
      .then((teardown) => {
        if (cancelled) {
          void teardown();
          return;
        }
        unsub = teardown;
        setActive(true);
      })
      .catch((err) => {
        if (cancelled) return;
        // Surface the failure as a synthetic killed event so the
        // consumer's UI can render the failure rather than silently
        // freezing.
        const reason = err instanceof Error ? err.message : "subscribe failed";
        const synthetic: LaneEvent = {
          kind: "killed",
          session_id: sessionId,
          lane_id: "",
          reason,
          at: new Date().toISOString(),
        };
        handlerRef.current(synthetic);
      });

    return () => {
      cancelled = true;
      setActive(false);
      if (unsub) {
        // Fire and forget; the closure is idempotent.
        void unsub();
      }
    };
  }, [sessionId, enabled]);

  return { active, eventsReceived };
}
