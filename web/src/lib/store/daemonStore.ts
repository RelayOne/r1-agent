// SPDX-License-Identifier: MIT
// Per-daemon zustand store factory. Spec §State / §Component Catalog.
//
// One **store instance per daemon connection** — daemons cannot share
// state. The application keeps a `Map<daemonId, DaemonStore>` and
// switches the active store when the user changes daemons via the
// left rail or Cmd+1..9.
//
// This file is the **composition root** for the per-daemon store.
// State and dispatchers are split across three sibling slice files
// per spec §Directory Layout (item 16):
//
//   - sessionsSlice.ts  — `sessions` state + session dispatchers
//   - lanesSlice.ts     — `lanes`    state + lane / tile dispatchers
//   - messagesSlice.ts  — `messages` state + ChatMessage type
//
// Cross-slice concerns that live here at the composition root:
//   1. `daemonId` + `settings` + `ui` slices (smaller; not split out).
//   2. `EnvelopeCoalescer`: rAF-coalesced WS envelope dispatch (D-S2 /
//      spec §WebSocket Reconnect Strategy).  `applyEnvelope` writes
//      to multiple slices in one transactional `set` and therefore
//      cannot live inside any single slice.
//   3. The exported public surface — `createDaemonStore`,
//      `getDaemonStore`, `dropDaemonStore`, `_resetDaemonRegistryForTests`,
//      and the `DaemonStore` / `DaemonState` types — is **identical**
//      to the pre-split version so consumers do not break.
import { create } from "zustand";
import type { StoreApi, UseBoundStore } from "zustand";
import type {
  DaemonId,
  LaneId,
  LaneState,
  SessionId,
  Settings,
  WsServerEnvelope,
} from "@/lib/api/types";
import {
  createSessionsSlice,
  type SessionsSlice,
} from "@/lib/store/sessionsSlice";
import {
  createLanesSlice,
  laneKey,
  type LanesSlice,
} from "@/lib/store/lanesSlice";
import {
  appendPart,
  createMessagesSlice,
  messageKey,
  type ChatMessage,
  type MessagesSlice,
} from "@/lib/store/messagesSlice";

// ---------------------------------------------------------------------------
// Re-exports — keep the public surface identical to the pre-split file.
// Existing imports such as
//     import type { ChatMessage } from "@/lib/store/daemonStore";
//     import type { SessionsSlice } from "@/lib/store/daemonStore";
// continue to compile without any consumer change.
// ---------------------------------------------------------------------------
export type { ChatMessage } from "@/lib/store/messagesSlice";
export type { SessionsSlice } from "@/lib/store/sessionsSlice";
export type { LanesSlice } from "@/lib/store/lanesSlice";
export type { MessagesSlice } from "@/lib/store/messagesSlice";

// ---------------------------------------------------------------------------
// Settings + UI slices (kept inline — small enough that splitting them
// out would only add noise; spec only calls out sessions/lanes/messages).
// ---------------------------------------------------------------------------

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
}

// ---------------------------------------------------------------------------
// Composed state shape — `DaemonState extends SessionsSlice & LanesSlice & MessagesSlice`
// plus the local settings/ui slices and the cross-slice dispatchers.
// ---------------------------------------------------------------------------

export interface DaemonState
  extends SessionsSlice,
    LanesSlice,
    MessagesSlice {
  daemonId: DaemonId;
  settings: SettingsSlice;
  ui: UiSlice;
  // Cross-slice dispatchers (kept on state for ergonomic selector access).
  applyEnvelope: (env: WsServerEnvelope) => void;
  hydrateSettings: (s: Settings) => void;
  setLeftRailCollapsed: (v: boolean) => void;
  setRightRailCollapsed: (v: boolean) => void;
  setTheme: (theme: UiSlice["theme"]) => void;
  setConnectionState: (s: UiSlice["connectionState"]) => void;
  setHardCapped: (v: boolean) => void;
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
  const baseStore = create<DaemonState>()((set, get, store) => {
    // Drain a coalesced batch into immutable state updates. We apply
    // the deltas to a local working copy then commit once.
    const drain = (batch: WsServerEnvelope[]): void => {
      set((prev) => {
        const sessions = {
          byId: { ...prev.sessions.byId },
          order: prev.sessions.order.slice(),
          subscribed: new Set(prev.sessions.subscribed),
          lastSeq: { ...prev.sessions.lastSeq },
          errorBySession: { ...prev.sessions.errorBySession },
        };
        const lanes = {
          byKey: { ...prev.lanes.byKey },
          orderBySession: { ...prev.lanes.orderBySession },
        };
        const messages = {
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
                const created: ChatMessage = {
                  id: env.messageId,
                  sessionId: env.sessionId,
                  role: env.role,
                  parts: [env.part],
                  streaming: true,
                  createdAt: env.ts,
                  updatedAt: env.ts,
                };
                messages.byKey[k] = created;
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
            case "error": {
              // Record the error string on the session it targets so
              // useChat can surface it via .error / .clearError.
              if (env.sessionId !== undefined) {
                sessions.errorBySession[env.sessionId] = env.message;
              }
              break;
            }
            case "auth.expiring_soon":
            case "pong":
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

    // Compose the three spec-mandated slices via the standard Zustand
    // StateCreator pattern. Each factory returns its own slice of the
    // total state; we spread them together below into the root.
    const sessionsSlice = createSessionsSlice(set, get, store);
    const lanesSlice = createLanesSlice(set, get, store);
    const messagesSlice = createMessagesSlice(set, get, store);

    return {
      ...sessionsSlice,
      ...lanesSlice,
      ...messagesSlice,

      daemonId,
      settings: emptySettingsSlice(),
      ui: emptyUiSlice(),

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
    };
  });

  // Tests use store.getState() / store.setState() directly via zustand.
  // Augment with a flushPending() helper for tests that need to drain
  // the rAF buffer synchronously.
  const store = baseStore as DaemonStore;
  store.flushPending = () => coalescerRef.flush();
  return store;
}

// ---------------------------------------------------------------------------
// Re-export the empty-state factories so test fixtures and storybooks
// that previously imported them from this module continue to work.
// (No production code imports these by name today, but the slice files
// are the canonical source.)
// ---------------------------------------------------------------------------
export {
  emptySessionsState as emptySessionsSlice,
} from "@/lib/store/sessionsSlice";
export {
  emptyLanesState as emptyLanesSlice,
} from "@/lib/store/lanesSlice";
export {
  emptyMessagesState as emptyMessagesSlice,
} from "@/lib/store/messagesSlice";

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
