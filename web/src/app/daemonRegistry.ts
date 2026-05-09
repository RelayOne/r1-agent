// SPDX-License-Identifier: MIT
// Daemon registry — maps a daemon id to its HTTP + WS endpoints. The
// registry is persisted in localStorage under the key `r1.daemons`,
// seeded with a single entry pointing at the local r1d on
// http://127.0.0.1:7777. The DaemonsLanding route lets the user add
// additional daemons by URL; the registry is the single source of
// truth for those entries.
//
// Spec ref: web-chat-ui Spec 6 (web chat UI mounting). Resolves the
// audit finding that the SPA entry rendered only a landing message
// while the component tree below it remained unmounted.
//
// Storage shape (localStorage key `r1.daemons`):
//
//   { "version": 1, "daemons": [{ id, name, baseUrl, wsUrl }, …] }
//
// We keep version/schema separate from `lib/api/types` because that
// file owns the wire-format DaemonInfo (which carries `status`). The
// registry only stores user-supplied connection info; status comes
// from the server / R1dClient.

export interface RegistryDaemon {
  /** Stable id used as a URL segment (`/d/:daemonId`). */
  id: string;
  /** Human-readable label for the landing page. */
  name: string;
  /** Origin (no trailing slash). */
  baseUrl: string;
  /** WebSocket endpoint (full URL, includes `/ws` path). */
  wsUrl: string;
}

const STORAGE_KEY = "r1.daemons";
const STORAGE_VERSION = 1;

interface PersistedRegistry {
  version: number;
  daemons: RegistryDaemon[];
}

/**
 * Default seed: one entry pointing at the local r1d daemon. Keep this
 * stable — the UI assumes `local` exists when no localStorage entries
 * have been written yet.
 */
export const DEFAULT_LOCAL_DAEMON: RegistryDaemon = {
  id: "local",
  name: "Local r1d",
  baseUrl: "http://127.0.0.1:7777",
  wsUrl: "ws://127.0.0.1:7777/ws",
};

function safeStorage(): Pick<Storage, "getItem" | "setItem"> | null {
  if (typeof window === "undefined") return null;
  try {
    // Probe — Safari private mode throws on setItem.
    window.localStorage.setItem(`${STORAGE_KEY}.__probe`, "1");
    window.localStorage.removeItem(`${STORAGE_KEY}.__probe`);
    return window.localStorage;
  } catch {
    return null;
  }
}

function isRegistryDaemon(v: unknown): v is RegistryDaemon {
  if (!v || typeof v !== "object") return false;
  const o = v as Record<string, unknown>;
  return (
    typeof o["id"] === "string" &&
    typeof o["name"] === "string" &&
    typeof o["baseUrl"] === "string" &&
    typeof o["wsUrl"] === "string"
  );
}

/** Load the current registry. Falls back to the default seed on any
 *  parse / storage error. */
export function loadRegistry(): RegistryDaemon[] {
  const storage = safeStorage();
  if (!storage) return [DEFAULT_LOCAL_DAEMON];
  let raw: string | null = null;
  try {
    raw = storage.getItem(STORAGE_KEY);
  } catch {
    return [DEFAULT_LOCAL_DAEMON];
  }
  if (!raw) return [DEFAULT_LOCAL_DAEMON];
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (
      parsed &&
      typeof parsed === "object" &&
      Array.isArray((parsed as PersistedRegistry).daemons)
    ) {
      const valid = (parsed as PersistedRegistry).daemons.filter(isRegistryDaemon);
      if (valid.length > 0) return valid;
    }
  } catch {
    // fallthrough to default
  }
  return [DEFAULT_LOCAL_DAEMON];
}

/** Persist the registry. No-op when storage is unavailable. */
export function saveRegistry(daemons: RegistryDaemon[]): void {
  const storage = safeStorage();
  if (!storage) return;
  const payload: PersistedRegistry = {
    version: STORAGE_VERSION,
    daemons,
  };
  try {
    storage.setItem(STORAGE_KEY, JSON.stringify(payload));
  } catch {
    // Quota / private mode — ignore silently; runtime still works
    // against the in-memory copy held by the UI components.
  }
}

/** Add a daemon. If `id` collides, the existing entry is replaced. */
export function addDaemon(d: RegistryDaemon): RegistryDaemon[] {
  const cur = loadRegistry();
  const filtered = cur.filter((x) => x.id !== d.id);
  const next = [...filtered, d];
  saveRegistry(next);
  return next;
}

/** Remove a daemon by id. */
export function removeDaemon(id: string): RegistryDaemon[] {
  const cur = loadRegistry();
  const next = cur.filter((x) => x.id !== id);
  saveRegistry(next);
  return next;
}

/**
 * Derive a `wsUrl` from a base http(s) URL. Used by the "Add daemon"
 * form so the user only types one URL. Defaults to `/ws` path.
 */
export function deriveWsUrl(baseUrl: string): string {
  try {
    const u = new URL(baseUrl);
    const proto = u.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${u.host}/ws`;
  } catch {
    // Fallback: assume same origin, plain ws.
    return "ws://127.0.0.1:7777/ws";
  }
}
