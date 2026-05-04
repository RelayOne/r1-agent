// SPDX-License-Identifier: MIT
// Per-daemon zustand store factory. Spec §State / §Component Catalog.
//
// One **store instance per daemon connection** — daemons cannot share
// state. The application keeps a `Map<daemonId, DaemonStore>` and
// switches the active store when the user changes daemons via the
// left rail or Cmd+1..9. This file provides:
//
//   1. The `DaemonState` shape (sessions / lanes / messages / settings / ui).
//   2. `createDaemonStore(daemonId, opts)` — a factory returning a
//      typed `UseBoundStore<StoreApi<DaemonState>>` plus the dispatch
//      helpers used by `useDaemonSocket` to route envelopes.
//   3. Re-render coalescing at 5–10 Hz via `flushRafBatch`. The store
//      buffers high-frequency `lane.delta` and `message.part` events
//      and flushes them on the next animation frame (D-S2 / spec
//      §WebSocket Reconnect Strategy anti-patterns).
//   4. Last-Event-ID per session for replay.
//
// The store carries no React-specific concerns; hooks consume it via
// `useStore(selector)` or the project-level `useDaemonStore` hook.
import { create } from "zustand";
import type { StoreApi, UseBoundStore } from "zustand";
import type {
  DaemonId,
  LaneId,
  LaneSnapshot,
  LaneState,
  MessagePart,
  SessionId,
  SessionMetadata,
  Settings,
  WsServerEnvelope,
} from "@/lib/api/types";

// ---------------------------------------------------------------------------
// State shape — five slices per spec §item 16
// ---------------------------------------------------------------------------

export interface ChatMessage {
  id: string;
  sessionId: SessionId;
  role: "assistant" | "user" | "system" | "tool";
  parts: MessagePart[];
  /** True until the server emits message.complete. */
  streaming: boolean;
  createdAt: string;
  updatedAt: string;
  costUsd?: number;
  durationMs?: number;
}

export interface SessionsSlice {
  /** All sessions known to the daemon, keyed by id. */
  byId: Record<SessionId, SessionMetadata>;
  /** Stable ordering for the SessionList sidebar. */
  order: SessionId[];
  /** Sessions currently subscribed to over WS. */
  subscribed: Set<SessionId>;
  /** Last-Event-ID seq per session for replay. */
  lastSeq: Record<SessionId, number>;
}

export interface LanesSlice {
  /** Lanes keyed by `${sessionId}:${laneId}`; stored flat to keep
   *  per-session iteration cheap. */
  byKey: Record<string, LaneSnapshot>;
  /** Per-session lane order (stable: creation timestamp + lane_id tiebreak). */
  orderBySession: Record<SessionId, LaneId[]>;
}

export interface MessagesSlice {
  /** Messages keyed by `${sessionId}:${messageId}`. */
  byKey: Record<string, ChatMessage>;
  /** Per-session message order. */
  orderBySession: Record<SessionId, string[]>;
}

export interface SettingsSlice {
  /** Server-persisted user settings, or null until /api/settings loads. */
  current: Settings | null;
  /** Last load attempt timestamp (debugging + retry policy). */
  loadedAt: string | null;
  /** Pending error, if the last load/save failed. */
  error: string | null;
}

export interface UiSlice {
  /** Pinned lane ids per session for the TileGrid. Order = display order. */
  tilePinnedBySession: Record<SessionId, LaneId[]>;
  /** Per-session pinned lane collapse flags (key=laneId → collapsed?). */
  tileCollapsedBySession: Record<SessionId, Record<LaneId, boolean>>;
  /** Sidebar collapse: separate flags for left + right rails. */
  leftRailCollapsed: boolean;
  rightRailCollapsed: boolean;
  /** Current theme. Persisted in localStorage by ThemeProvider. */
  theme: "light" | "dark" | "hc" | "system";
  /** WS connection state mirrored from ResilientSocket. */
  connectionState:
    | "idle"
    | "connecting"
    | "open"
    | "reconnecting"
    | "closed";
  /** Surfaces hard-cap reconnect failures to the ConnectionLostBanner. */
  hardCapped: boolean;
  /** When tileMode is non-empty, ChatPane swaps to TileGrid (item 25). */
  // (derived from tilePinnedBySession.length > 0 — kept here for tests.)
}

export interface DaemonState {
  daemonId: DaemonId;
  sessions: SessionsSlice;
  lanes: LanesSlice;
  messages: MessagesSlice;
  settings: SettingsSlice;
  ui: UiSlice;
  // Dispatchers (kept on state for ergonomic selector access).
  applyEnvelope: (env: WsServerEnvelope) => void;
  hydrateSessions: (rows: SessionMetadata[]) => void;
  hydrateLanes: (sessionId: SessionId, lanes: LaneSnapshot[]) => void;
  hydrateSettings: (s: Settings) => void;
  setLeftRailCollapsed: (v: boolean) => void;
  setRightRailCollapsed: (v: boolean) => void;
  setTheme: (theme: UiSlice["theme"]) => void;
  setConnectionState: (s: UiSlice["connectionState"]) => void;
  setHardCapped: (v: boolean) => void;
  pinLane: (sessionId: SessionId, laneId: LaneId) => void;
  unpinLane: (sessionId: SessionId, laneId: LaneId) => void;
  reorderTiles: (sessionId: SessionId, ids: LaneId[]) => void;
  toggleTileCollapsed: (sessionId: SessionId, laneId: LaneId) => void;
  markSubscribed: (sessionId: SessionId) => void;
  markUnsubscribed: (sessionId: SessionId) => void;
}

// ---------------------------------------------------------------------------
// Coalescing scheduler — buffers high-frequency envelopes and flushes
// once per animation frame (D-S2). Falls back to setTimeout(16) when
// rAF is not available (e.g. Node test env).
// ---------------------------------------------------------------------------

export interface CoalescerOptions {
  /** Test injection. Default: globalThis.requestAnimationFrame. */
  schedule?: (fn: () => void) => unknown;
  /** Test injection. Default: globalThis.cancelAnimationFrame. */
  cancel?: (handle: unknown) => void;
}

function defaultSchedule(fn: () => void): unknown {
  if (typeof globalThis.requestAnimationFrame === "function") {
    return globalThis.requestAnimationFrame(fn);
  }
  return setTimeout(fn, 16);
}
function defaultCancel(handle: unknown): void {
  if (typeof globalThis.cancelAnimationFrame === "function" && typeof handle === "number") {
    globalThis.cancelAnimationFrame(handle);
    return;
  }
  if (typeof handle === "number") clearTimeout(handle as unknown as ReturnType<typeof setTimeout>);
}

export class EnvelopeCoalescer {
  private buffer: WsServerEnvelope[] = [];
  private handle: unknown;
  private readonly schedule: (fn: () => void) => unknown;
  private readonly cancel: (handle: unknown) => void;

  constructor(
    private readonly drain: (events: WsServerEnvelope[]) => void,
    opts: CoalescerOptions = {},
  ) {
    this.schedule = opts.schedule ?? defaultSchedule;
    this.cancel = opts.cancel ?? defaultCancel;
  }

  push(env: WsServerEnvelope): void {
    this.buffer.push(env);
    if (this.handle === undefined) {
      this.handle = this.schedule(() => this.flush());
    }
  }

  /** Flush synchronously — used by tests and by message.complete to
   *  guarantee the terminal envelope lands in the same tick. */
  flush(): void {
    if (this.handle !== undefined) {
      this.cancel(this.handle);
      this.handle = undefined;
    }
    if (this.buffer.length === 0) return;
    const drained = this.buffer;
    this.buffer = [];
    this.drain(drained);
  }
}

// ---------------------------------------------------------------------------
// Store factory
// ---------------------------------------------------------------------------

export interface CreateDaemonStoreOptions {
  /** Inject the rAF scheduler used by the internal coalescer. Default:
   *  globalThis.requestAnimationFrame falling back to setTimeout(16).
   *  Tests pass a no-op scheduler and call `flushPending()` to drain. */
  schedule?: (fn: () => void) => unknown;
  cancel?: (handle: unknown) => void;
}

export type DaemonStore = UseBoundStore<StoreApi<DaemonState>> & {
  /** Test hook: drain any buffered envelopes synchronously. */
  flushPending: () => void;
};

const laneKey = (sid: SessionId, lid: LaneId): string => `${sid}:${lid}`;
const messageKey = (sid: SessionId, mid: string): string => `${sid}:${mid}`;

function emptySessionsSlice(): SessionsSlice {
  return {
    byId: {},
    order: [],
    subscribed: new Set<SessionId>(),
    lastSeq: {},
  };
}
function emptyLanesSlice(): LanesSlice {
  return { byKey: {}, orderBySession: {} };
}
function emptyMessagesSlice(): MessagesSlice {
  return { byKey: {}, orderBySession: {} };
}
function emptySettingsSlice(): SettingsSlice {
  return { current: null, loadedAt: null, error: null };
}
function emptyUiSlice(): UiSlice {
  return {
    tilePinnedBySession: {},
    tileCollapsedBySession: {},
    leftRailCollapsed: false,
    rightRailCollapsed: false,
    theme: "system",
    connectionState: "idle",
    hardCapped: false,
  };
}

export function createDaemonStore(
  daemonId: DaemonId,
  opts: CreateDaemonStoreOptions = {},
): DaemonStore {
  // Coalescer reference is captured below; declared up-front so the
  // store's `applyEnvelope` closure can call it.
  let coalescerRef: EnvelopeCoalescer;
  const baseStore = create<DaemonState>()((set) => {
    // Drain a coalesced batch into immutable state updates. We apply
    // the deltas to a local working copy then commit once.
    const drain = (batch: WsServerEnvelope[]): void => {
      set((prev) => {
        const sessions: SessionsSlice = {
          byId: { ...prev.sessions.byId },
          order: prev.sessions.order.slice(),
          subscribed: new Set(prev.sessions.subscribed),
          lastSeq: { ...prev.sessions.lastSeq },
        };
        const lanes: LanesSlice = {
          byKey: { ...prev.lanes.byKey },
          orderBySession: { ...prev.lanes.orderBySession },
        };
        const messages: MessagesSlice = {
          byKey: { ...prev.messages.byKey },
          orderBySession: { ...prev.messages.orderBySession },
        };
        for (const env of batch) {
          // Track lastSeq per session for replay.
          const sid = "sessionId" in env && env.sessionId ? env.sessionId : undefined;
          if (sid !== undefined) {
            const cur = sessions.lastSeq[sid] ?? -1;
            if (env.seq > cur) sessions.lastSeq[sid] = env.seq;
          }
          switch (env.type) {
            case "lane.created": {
              const k = laneKey(env.sessionId, env.lane.id);
              lanes.byKey[k] = env.lane;
              const ord = (lanes.orderBySession[env.sessionId] ?? []).slice();
              if (!ord.includes(env.lane.id)) ord.push(env.lane.id);
              lanes.orderBySession[env.sessionId] = ord;
              break;
            }
            case "lane.killed": {
              const k = laneKey(env.sessionId, env.laneId);
              const cur = lanes.byKey[k];
              if (cur) {
                lanes.byKey[k] = { ...cur, state: "killed" satisfies LaneState };
              }
              break;
            }
            case "lane.status": {
              const k = laneKey(env.sessionId, env.laneId);
              const cur = lanes.byKey[k];
              if (cur) {
                const progress = env.progress === undefined ? cur.progress : env.progress;
                lanes.byKey[k] = { ...cur, state: env.state, progress, lastSeq: env.seq };
              }
              break;
            }
            case "lane.delta": {
              const k = laneKey(env.sessionId, env.laneId);
              const cur = lanes.byKey[k];
              if (cur) {
                lanes.byKey[k] = {
                  ...cur,
                  lastRender: (cur.lastRender ?? "") + env.data,
                  lastSeq: env.seq,
                  updatedAt: env.ts,
                };
              }
              break;
            }
            case "message.part": {
              const k = messageKey(env.sessionId, env.messageId);
              const cur = messages.byKey[k];
              if (cur) {
                messages.byKey[k] = {
                  ...cur,
                  parts: appendPart(cur.parts, env.part),
                  updatedAt: env.ts,
                };
              } else {
                messages.byKey[k] = {
                  id: env.messageId,
                  sessionId: env.sessionId,
                  role: env.role,
                  parts: [env.part],
                  streaming: true,
                  createdAt: env.ts,
                  updatedAt: env.ts,
                };
                const ord = (messages.orderBySession[env.sessionId] ?? []).slice();
                if (!ord.includes(env.messageId)) ord.push(env.messageId);
                messages.orderBySession[env.sessionId] = ord;
              }
              break;
            }
            case "message.complete": {
              const k = messageKey(env.sessionId, env.messageId);
              const cur = messages.byKey[k];
              if (cur) {
                messages.byKey[k] = {
                  ...cur,
                  streaming: false,
                  ...(env.costUsd !== undefined && { costUsd: env.costUsd }),
                  ...(env.durationMs !== undefined && { durationMs: env.durationMs }),
                  updatedAt: env.ts,
                };
              }
              break;
            }
            case "session.updated": {
              const cur = sessions.byId[env.sessionId];
              if (cur) {
                sessions.byId[env.sessionId] = { ...cur, ...env.patch };
              }
              break;
            }
            case "auth.expiring_soon":
            case "pong":
            case "error":
              // Handled by ResilientSocket / hook layer; no state mutation.
              break;
          }
        }
        return { ...prev, sessions, lanes, messages };
      });
    };

    coalescerRef = new EnvelopeCoalescer(drain, {
      ...(opts.schedule !== undefined && { schedule: opts.schedule }),
      ...(opts.cancel !== undefined && { cancel: opts.cancel }),
    });

    const baseState: Pick<DaemonState,
      "daemonId" | "sessions" | "lanes" | "messages" | "settings" | "ui"
    > = {
      daemonId,
      sessions: emptySessionsSlice(),
      lanes: emptyLanesSlice(),
      messages: emptyMessagesSlice(),
      settings: emptySettingsSlice(),
      ui: emptyUiSlice(),
    };

    return {
      ...baseState,

      applyEnvelope: (env) => {
        // Terminal-like events flush synchronously so consumers see
        // the canonical final state immediately. Lifecycle events
        // (lane.created/killed) also flush so the LanesSidebar list
        // updates without waiting for the next rAF.
        coalescerRef.push(env);
        if (
          env.type === "message.complete" ||
          env.type === "lane.created" ||
          env.type === "lane.killed" ||
          env.type === "session.updated"
        ) {
          coalescerRef.flush();
        }
      },

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

      hydrateLanes: (sessionId, lanesArr) =>
        set((prev) => {
          const byKey = { ...prev.lanes.byKey };
          const order: LaneId[] = [];
          for (const l of lanesArr) {
            byKey[laneKey(sessionId, l.id)] = l;
            order.push(l.id);
          }
          return {
            ...prev,
            lanes: {
              byKey,
              orderBySession: { ...prev.lanes.orderBySession, [sessionId]: order },
            },
          };
        }),

      hydrateSettings: (s) =>
        set((prev) => ({
          ...prev,
          settings: { current: s, loadedAt: new Date().toISOString(), error: null },
        })),

      setLeftRailCollapsed: (v) =>
        set((prev) => ({ ...prev, ui: { ...prev.ui, leftRailCollapsed: v } })),

      setRightRailCollapsed: (v) =>
        set((prev) => ({ ...prev, ui: { ...prev.ui, rightRailCollapsed: v } })),

      setTheme: (theme) => set((prev) => ({ ...prev, ui: { ...prev.ui, theme } })),

      setConnectionState: (s) =>
        set((prev) => ({ ...prev, ui: { ...prev.ui, connectionState: s } })),

      setHardCapped: (v) => set((prev) => ({ ...prev, ui: { ...prev.ui, hardCapped: v } })),

      pinLane: (sessionId, laneId) =>
        set((prev) => {
          const cur = prev.ui.tilePinnedBySession[sessionId] ?? [];
          if (cur.includes(laneId)) return prev;
          return {
            ...prev,
            ui: {
              ...prev.ui,
              tilePinnedBySession: {
                ...prev.ui.tilePinnedBySession,
                [sessionId]: [...cur, laneId],
              },
            },
          };
        }),

      unpinLane: (sessionId, laneId) =>
        set((prev) => {
          const cur = prev.ui.tilePinnedBySession[sessionId] ?? [];
          const next = cur.filter((x) => x !== laneId);
          const collapsedCur = prev.ui.tileCollapsedBySession[sessionId] ?? {};
          const { [laneId]: _drop, ...collapsedNext } = collapsedCur;
          void _drop;
          return {
            ...prev,
            ui: {
              ...prev.ui,
              tilePinnedBySession: {
                ...prev.ui.tilePinnedBySession,
                [sessionId]: next,
              },
              tileCollapsedBySession: {
                ...prev.ui.tileCollapsedBySession,
                [sessionId]: collapsedNext,
              },
            },
          };
        }),

      reorderTiles: (sessionId, ids) =>
        set((prev) => ({
          ...prev,
          ui: {
            ...prev.ui,
            tilePinnedBySession: { ...prev.ui.tilePinnedBySession, [sessionId]: ids },
          },
        })),

      toggleTileCollapsed: (sessionId, laneId) =>
        set((prev) => {
          const cur = prev.ui.tileCollapsedBySession[sessionId] ?? {};
          return {
            ...prev,
            ui: {
              ...prev.ui,
              tileCollapsedBySession: {
                ...prev.ui.tileCollapsedBySession,
                [sessionId]: { ...cur, [laneId]: !cur[laneId] },
              },
            },
          };
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
    };
  });

  // Tests use store.getState() / store.setState() directly via zustand.
  // Augment with a flushPending() helper for tests that need to drain
  // the rAF buffer synchronously.
  const store = baseStore as DaemonStore;
  store.flushPending = () => coalescerRef.flush();
  return store;
}

// Helper: append/merge a streamed message part. Text parts coalesce
// (the same part kind streamed multiple times accumulates text);
// other kinds replace by toolCallId / index.
function appendPart(existing: MessagePart[], next: MessagePart): MessagePart[] {
  if (next.kind === "text") {
    const last = existing[existing.length - 1];
    if (last && last.kind === "text") {
      const merged: MessagePart = { kind: "text", text: last.text + next.text };
      return [...existing.slice(0, -1), merged];
    }
    return [...existing, next];
  }
  if (next.kind === "tool") {
    const idx = existing.findIndex(
      (p) => p.kind === "tool" && p.toolCallId === next.toolCallId,
    );
    if (idx >= 0) {
      const copy = existing.slice();
      copy[idx] = next;
      return copy;
    }
    return [...existing, next];
  }
  if (next.kind === "reasoning") {
    const idx = existing.findIndex((p) => p.kind === "reasoning");
    if (idx >= 0) {
      const copy = existing.slice();
      copy[idx] = next;
      return copy;
    }
    return [...existing, next];
  }
  if (next.kind === "plan") {
    const idx = existing.findIndex((p) => p.kind === "plan");
    if (idx >= 0) {
      const copy = existing.slice();
      copy[idx] = next;
      return copy;
    }
    return [...existing, next];
  }
  return [...existing, next];
}

// ---------------------------------------------------------------------------
// Multi-daemon registry — `Map<daemonId, DaemonStore>`
// ---------------------------------------------------------------------------

const REGISTRY = new Map<DaemonId, DaemonStore>();

/** Get or create the store for a daemon. Idempotent. */
export function getDaemonStore(
  daemonId: DaemonId,
  opts: CreateDaemonStoreOptions = {},
): DaemonStore {
  let s = REGISTRY.get(daemonId);
  if (!s) {
    s = createDaemonStore(daemonId, opts);
    REGISTRY.set(daemonId, s);
  }
  return s;
}

/** Drop a store (e.g. on daemon disconnect or for tests). */
export function dropDaemonStore(daemonId: DaemonId): void {
  REGISTRY.delete(daemonId);
}

/** Test-only: clear the registry. */
export function _resetDaemonRegistryForTests(): void {
  REGISTRY.clear();
}
