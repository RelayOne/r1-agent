// SPDX-License-Identifier: MIT
// Sessions slice — per spec §Directory Layout (item 16).
//
// This file owns the **session-shaped** portion of the per-daemon
// store: the `sessions` state record plus session-related dispatchers.
// It is composed into the root `DaemonState` by `daemonStore.ts` via
// the standard Zustand StateCreator slice pattern:
//
//     export const createSessionsSlice:
//       StateCreator<DaemonState, [], [], SessionsSlice> =
//       (set, get, store) => ({ ... });
//
// Cross-slice envelope dispatch (e.g. `applyEnvelope`) lives at the
// composition root because it touches multiple slices in one `set`.
import type { StateCreator } from "zustand";
import type {
  SessionId,
  SessionMetadata,
} from "@/lib/api/types";
import type { DaemonState } from "@/lib/store/daemonStore";

/** Internal shape of the `sessions` sub-state. Kept as a nested
 *  record so the existing public test contract (`s.sessions.byId`,
 *  `s.sessions.order`, …) is preserved verbatim. */
export interface SessionsState {
  /** All sessions known to the daemon, keyed by id. */
  byId: Record<SessionId, SessionMetadata>;
  /** Stable ordering for the SessionList sidebar. */
  order: SessionId[];
  /** Sessions currently subscribed to over WS. */
  subscribed: Set<SessionId>;
  /** Last-Event-ID seq per session for replay. */
  lastSeq: Record<SessionId, number>;
  /** Per-session error string, populated by error envelopes / hooks
   *  and cleared via setSessionError(sid, undefined). */
  errorBySession: Record<SessionId, string>;
}

export interface SessionsSlice {
  sessions: SessionsState;
  /** Bulk-load sessions from `/api/sessions`; appends in stable insertion
   *  order with dedupe. */
  hydrateSessions: (rows: SessionMetadata[]) => void;
  /** Mark a session as subscribed over the WS. */
  markSubscribed: (sessionId: SessionId) => void;
  /** Mark a session as unsubscribed over the WS. */
  markUnsubscribed: (sessionId: SessionId) => void;
  /** Record an error string for a session; pass undefined to clear. */
  setSessionError: (sessionId: SessionId, error: string | undefined) => void;
}

/** Empty initial state for the sessions sub-record. Exported so the
 *  root composer and tests can reset cleanly. */
export function emptySessionsState(): SessionsState {
  return {
    byId: {},
    order: [],
    subscribed: new Set<SessionId>(),
    lastSeq: {},
    errorBySession: {},
  };
}

export const createSessionsSlice: StateCreator<
  DaemonState,
  [],
  [],
  SessionsSlice
> = (set) => ({
  sessions: emptySessionsState(),

  hydrateSessions: (rows) =>
    set((prev) => {
      const byId: Record<SessionId, SessionMetadata> = { ...prev.sessions.byId };
      const order: SessionId[] = prev.sessions.order.slice();
      for (const r of rows) {
        byId[r.id] = r;
        if (!order.includes(r.id)) order.push(r.id);
      }
      return { ...prev, sessions: { ...prev.sessions, byId, order } };
    }),

  markSubscribed: (sessionId) =>
    set((prev) => {
      const next = new Set(prev.sessions.subscribed);
      next.add(sessionId);
      return { ...prev, sessions: { ...prev.sessions, subscribed: next } };
    }),

  markUnsubscribed: (sessionId) =>
    set((prev) => {
      const next = new Set(prev.sessions.subscribed);
      next.delete(sessionId);
      return { ...prev, sessions: { ...prev.sessions, subscribed: next } };
    }),

  setSessionError: (sessionId, error) =>
    set((prev) => {
      const nextErr = { ...prev.sessions.errorBySession };
      if (error === undefined) {
        delete nextErr[sessionId];
      } else {
        nextErr[sessionId] = error;
      }
      return {
        ...prev,
        sessions: { ...prev.sessions, errorBySession: nextErr },
      };
    }),
});
