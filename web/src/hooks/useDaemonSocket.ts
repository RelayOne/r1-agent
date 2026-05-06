// SPDX-License-Identifier: MIT
// useDaemonSocket — React hook that owns the ResilientSocket lifecycle
// for a daemon and routes every server envelope into the daemon's
// zustand store via `applyEnvelope`. Spec item 17/55.
//
// One hook instance per daemon. The hook:
//   1. Mints a WS ticket via R1dClient.
//   2. Constructs a ResilientSocket and connects.
//   3. Forwards onEnvelope -> store.applyEnvelope (which coalesces).
//   4. Forwards onStateChange -> store.setConnectionState.
//   5. Forwards onHardCap -> store.setHardCapped(true).
//   6. Re-subscribes all sessions in `store.sessions.subscribed` on
//      every (re)connect with the per-session lastSeq from
//      `store.sessions.lastSeq` (Last-Event-ID replay).
//   7. Cleans up on unmount with a deliberate close (code 1000).
import { useCallback, useEffect, useRef } from "react";
import type { R1dClient } from "@/lib/api/r1d";
import type { DaemonStore } from "@/lib/store/daemonStore";
import type {
  SessionId,
  WsServerEnvelope,
} from "@/lib/api/types";

export interface UseDaemonSocketOptions {
  /** The R1dClient bound to this daemon's HTTP+WS endpoints. */
  client: R1dClient;
  /** The daemon's zustand store (from getDaemonStore(daemonId)). */
  store: DaemonStore;
  /** Disable auto-connect (tests, lazy mode). Default true. */
  autoConnect?: boolean;
  /** Called when the server emits a top-level `error` envelope. */
  onError?: (env: Extract<WsServerEnvelope, { type: "error" }>) => void;
  /** Called when a server frame fails zod schema validation. */
  onSchemaError?: (raw: unknown, message: string) => void;
}

export interface UseDaemonSocketReturn {
  /** Subscribe to a session over WS; lastSeq comes from the store. */
  subscribe: (sessionId: SessionId) => void;
  /** Unsubscribe a session. */
  unsubscribe: (sessionId: SessionId) => void;
  /** Send a chat message (free text) on a subscribed session. */
  sendMessage: (sessionId: SessionId, content: string) => void;
  /** Cancel the current turn (drop-partial protocol). */
  interrupt: (sessionId: SessionId) => void;
  /** Force a connect (idempotent; useful when autoConnect is false). */
  connect: () => Promise<void>;
  /** Force a clean close. */
  disconnect: () => void;
}

/**
 * Wires the ResilientSocket lifecycle to the daemon store. Returns
 * stable callbacks for subscribe / send / interrupt that components
 * use without needing to know about the underlying socket. Tests can
 * pass `autoConnect: false` to keep the socket dormant.
 */
export function useDaemonSocket(opts: UseDaemonSocketOptions): UseDaemonSocketReturn {
  const { client, store, autoConnect = true, onError, onSchemaError } = opts;

  // Stable refs so the socket-side callbacks always read the latest
  // store/client without forcing reconnects when consumers re-render.
  const clientRef = useRef(client);
  const storeRef = useRef(store);
  const onErrorRef = useRef(onError);
  const onSchemaErrorRef = useRef(onSchemaError);
  clientRef.current = client;
  storeRef.current = store;
  onErrorRef.current = onError;
  onSchemaErrorRef.current = onSchemaError;

  // Connect/disconnect helpers.
  const connect = useCallback(async (): Promise<void> => {
    const c = clientRef.current;
    const s = storeRef.current;
    await c.connect({
      onEnvelope: (env: WsServerEnvelope) => {
        // Route into the store. The store's coalescer batches at 5–10 Hz.
        s.getState().applyEnvelope(env);
        // Also surface top-level error envelopes via the optional callback.
        if (env.type === "error") {
          onErrorRef.current?.(env);
        }
      },
      getLastEventId: (sessionId) => s.getState().sessions.lastSeq[sessionId],
      getSubscribedSessions: () => Array.from(s.getState().sessions.subscribed),
      onStateChange: (state) => {
        s.getState().setConnectionState(state);
        if (state === "open") {
          s.getState().setHardCapped(false);
        }
      },
      onHardCap: () => {
        s.getState().setHardCapped(true);
      },
      onSchemaError: (zerr, raw) => {
        onSchemaErrorRef.current?.(raw, zerr.message);
      },
    });
  }, []);

  const disconnect = useCallback(() => {
    clientRef.current.close();
  }, []);

  // Auto-connect on mount; clean up on unmount.
  useEffect(() => {
    if (!autoConnect) return;
    let cancelled = false;
    void connect().catch(() => {
      // Errors surface via onStateChange / onHardCap; the promise
      // rejection here is the initial-connect failure, which the
      // ResilientSocket also retries in the background.
    });
    return () => {
      cancelled = true;
      // Mark as closed for state machine purposes.
      try {
        clientRef.current.close();
      } catch {
        // Already closed during reconnect race; ignore.
      }
      void cancelled;
    };
  }, [autoConnect, connect]);

  // Action wrappers that update the store's `subscribed` set in
  // lockstep with the WS frames so reconnect replay sees the right
  // session list.
  const subscribe = useCallback((sessionId: SessionId): void => {
    const s = storeRef.current.getState();
    s.markSubscribed(sessionId);
    const lastEventId = s.sessions.lastSeq[sessionId];
    try {
      clientRef.current.subscribe(sessionId, lastEventId);
    } catch {
      // Socket not yet open; the next connect() will replay the
      // subscribe via getSubscribedSessions().
    }
  }, []);

  const unsubscribe = useCallback((sessionId: SessionId): void => {
    storeRef.current.getState().markUnsubscribed(sessionId);
    try {
      clientRef.current.unsubscribe(sessionId);
    } catch {
      // Socket not open; the unsubscribe is already reflected in store.
    }
  }, []);

  const sendMessage = useCallback((sessionId: SessionId, content: string): void => {
    clientRef.current.sendMessage(sessionId, content);
  }, []);

  const interrupt = useCallback((sessionId: SessionId): void => {
    clientRef.current.interrupt(sessionId);
  }, []);

  return { subscribe, unsubscribe, sendMessage, interrupt, connect, disconnect };
}
